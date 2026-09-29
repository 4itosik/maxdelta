// Package webhook — доставка событий на подписки ботов.
//
// Гарантии: события одного диалога уходят строго по порядку (по очереди на
// chat_id), разные диалоги обрабатываются параллельно; ни одно тело не уходит
// на стенд, не пройдя валидацию против openapi.MaxBotWebhook.yaml; каждая
// попытка попадает в журнал доставок.
package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"maxmock/internal/config"
	"maxmock/internal/events"
	"maxmock/internal/logx"
	"maxmock/internal/specs"
	"maxmock/internal/store"
	"maxmock/internal/tlsconf"
)

// queueCapacity — глубина очереди одного диалога. Доставки идут на порядки
// быстрее, чем тестировщик кликает, поэтому упереться в неё можно только при
// неотвечающем стенде — тогда обратное давление уместнее потери событий.
const queueCapacity = 256

// maxResponseBody — потолок на тело ответа стенда, попадающее в журнал.
// Упавший стенд отвечает HTML-страницей ошибки, и без ограничения она осела
// бы в базе на каждой попытке каждого события.
const maxResponseBody = 8 << 10

// userAgent — User-Agent, с которым приходят вебхуки живого MAX (снято
// 2026-09-29 с пяти событий трёх типов; «OneMe» — внутреннее имя MAX, его же
// домен oneme.ru в ссылках на файлы). Без явного значения Go подставлял
// Go-http-client/1.1, и стенд за периметром видел мок не так, как прод.
const userAgent = "OneMe/0.1.10 Bot API"

// postResult — исход одной попытки доставки.
type postResult struct {
	Status   int
	Body     string
	Duration time.Duration
}

type job struct {
	botID      int64
	chatID     int64
	updateType string
	body       []byte
}

// Dispatcher доставляет события подписчикам.
type Dispatcher struct {
	store  *store.Store
	specs  *specs.Specs
	bus    *events.Bus
	cfg    config.Webhook
	client *http.Client

	mu     sync.Mutex
	queues map[int64]chan job
	closed bool
	wg     sync.WaitGroup // задачи в очередях
	work   sync.WaitGroup // работающие обработчики очередей
}

// New создаёт диспетчер.
//
// Ошибка возможна только на настройках доверия к стендам: нечитаемый или
// пустой CA-файл. Она возвращается, а не игнорируется, потому что иначе
// расхождение проявилось бы как «события не приходят» через час после
// старта.
func New(st *store.Store, sp *specs.Specs, bus *events.Bus, cfg config.Webhook) (*Dispatcher, error) {
	timeout := time.Duration(cfg.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	tlsCfg, err := tlsconf.ClientConfig(cfg.CAFile, cfg.InsecureSkipVerify)
	if err != nil {
		return nil, fmt.Errorf("доставка вебхуков: %w", err)
	}
	// Клонируем транспорт по умолчанию, а не собираем свой: так сохраняются
	// пул соединений, таймауты установки и поддержка прокси из окружения.
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = tlsCfg

	return &Dispatcher{
		store:  st,
		specs:  sp,
		bus:    bus,
		cfg:    cfg,
		client: &http.Client{Timeout: timeout, Transport: tr},
		queues: make(map[int64]chan job),
	}, nil
}

// Deliver ставит событие в очередь диалога.
//
// Тело сериализуется и валидируется синхронно: невалидное событие — это
// ошибка мока, и она должна быть видна сразу, а не «когда-нибудь в журнале».
// Возвращаемая ошибка предназначена для логирования вызывающим кодом; сама
// доставка идёт асинхронно.
func (d *Dispatcher) Deliver(botID, chatID int64, updateType string, update any) error {
	body, err := json.Marshal(update)
	if err != nil {
		return fmt.Errorf("сериализация %s: %w", updateType, err)
	}
	if err := d.specs.ValidateWebhookBody(context.Background(), body); err != nil {
		d.logRejected(botID, chatID, updateType, body, err)
		return fmt.Errorf("событие %s не соответствует webhook-контракту: %w", updateType, err)
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return fmt.Errorf("диспетчер остановлен")
	}
	q, ok := d.queues[chatID]
	if !ok {
		q = make(chan job, queueCapacity)
		d.queues[chatID] = q
		d.work.Add(1)
		go d.runQueue(q)
	}
	d.wg.Add(1)
	q <- job{botID: botID, chatID: chatID, updateType: updateType, body: body}
	return nil
}

// logRejected фиксирует отказ от доставки невалидного события.
func (d *Dispatcher) logRejected(botID, chatID int64, updateType string, body []byte, cause error) {
	e := &store.DeliveryEntry{
		BotID: botID, ChatID: &chatID, URL: "", UpdateType: updateType, Body: string(body),
		Attempt: 0, Status: 0,
		Error: "событие не отправлено: не прошло валидацию по webhook-контракту: " + cause.Error(),
	}
	_ = d.store.LogDelivery(e)
	slog.LogAttrs(context.Background(), slog.LevelError, "событие не прошло webhook-контракт",
		slog.String("type", updateType),
		logx.Int64("bot", botID),
		logx.Int64("chat", chatID),
		logx.Err(cause),
		logx.Body("event", body),
	)
	// ChatID — иначе фронт сочтёт отвергнутое валидацией событие «без
	// диалога» (см. logSkipped и deliverTo, которые его проставляют) и
	// перечитает ленту не того диалога, что открыт у тестировщика.
	d.bus.Publish(events.Event{Kind: events.KindDelivery, BotID: botID, ChatID: chatID, Payload: e})
}

func (d *Dispatcher) runQueue(q chan job) {
	defer d.work.Done()
	for j := range q {
		d.process(j)
		d.wg.Done()
	}
}

func (d *Dispatcher) process(j job) {
	subs, err := d.store.ListSubscriptions(j.botID)
	if err != nil {
		return
	}
	delivered := false
	for _, sub := range subs {
		if !sub.Wants(j.updateType) {
			continue
		}
		delivered = true
		d.deliverTo(sub, j)
	}
	if !delivered {
		d.logSkipped(j)
	}
}

// logSkipped фиксирует событие, которое сгенерировано, но никуда не ушло:
// подписки на этот тип нет либо подписок нет вовсе. Без такой записи событие
// пропадает бесследно, и «мок не отправил» выглядит неотличимо от «стенд не
// подписан» — на этом разбор интеграции и застревает.
func (d *Dispatcher) logSkipped(j job) {
	e := &store.DeliveryEntry{
		BotID: j.botID, ChatID: &j.chatID, UpdateType: j.updateType,
		Body: string(j.body), Error: "нет подписки на этот тип события",
	}
	slog.LogAttrs(context.Background(), slog.LevelWarn, "событие никуда не ушло",
		slog.String("type", j.updateType),
		logx.Int64("bot", j.botID),
		logx.Int64("chat", j.chatID),
		slog.String("причина", "нет подписки на этот тип события"),
	)
	if err := d.store.LogDelivery(e); err != nil {
		return
	}
	d.bus.Publish(events.Event{
		Kind: events.KindDelivery, BotID: j.botID, ChatID: j.chatID, Payload: e,
	})
}

// deliverTo доставляет одно событие одной подписке с повторами.
func (d *Dispatcher) deliverTo(sub store.Subscription, j job) {
	attempts := d.cfg.Retries + 1
	if attempts < 1 {
		attempts = 1
	}
	for attempt := 1; attempt <= attempts; attempt++ {
		res, err := d.post(sub, j.body)
		entry := &store.DeliveryEntry{
			BotID: j.botID, ChatID: &j.chatID, SubscriptionID: &sub.ID, URL: sub.URL,
			UpdateType: j.updateType, Body: string(j.body),
			Attempt: attempt, Status: res.Status, ResponseBody: res.Body,
			DurationMS: res.Duration.Milliseconds(),
		}
		if err != nil {
			entry.Error = err.Error()
		}
		logDelivery(sub, j, attempt, attempts, res, err)
		_ = d.store.LogDelivery(entry)
		d.bus.Publish(events.Event{
			Kind: events.KindDelivery, BotID: j.botID, ChatID: j.chatID, Payload: entry,
		})

		// Решение о повторе принимается по статусу, а не по наличию ошибки:
		// 2xx с оборванным чтением тела означает, что стенд событие принял, и
		// повтор его продублирует. Сама ошибка при этом остаётся в журнале.
		// Сетевой сбой статуса не даёт вовсе (0) — такая попытка повторяется,
		// как и раньше.
		if res.Status >= 200 && res.Status < 300 {
			return
		}
		// 4xx — стенд осознанно отверг событие, повтор ничего не изменит.
		if res.Status >= 400 && res.Status < 500 {
			return
		}
		if attempt < attempts {
			time.Sleep(d.backoff(attempt))
		}
	}
}

// logDelivery печатает одну попытку доставки.
//
// Секрет подписки в строку не попадает: он уходит заголовком
// X-Max-Bot-Api-Secret, а заголовки здесь не печатаются вовсе. Номер попытки
// печатается вместе с их общим числом — «2» без «из 3» не отвечает на вопрос,
// будет ли ещё одна.
func logDelivery(sub store.Subscription, j job, attempt, attempts int, res postResult, err error) {
	slog.LogAttrs(context.Background(), logx.LevelForStatus(res.Status), "вебхук →",
		slog.String("type", j.updateType),
		slog.String("url", sub.URL),
		slog.Int("status", res.Status),
		slog.Int64("ms", res.Duration.Milliseconds()),
		slog.String("попытка", fmt.Sprintf("%d/%d", attempt, attempts)),
		logx.Int64("bot", j.botID),
		logx.Int64("chat", j.chatID),
		logx.Err(err),
		logx.Body("event", j.body),
		logx.Body("resp", []byte(res.Body)),
	)
}

// backoff возвращает паузу перед повтором номер attempt+1.
func (d *Dispatcher) backoff(attempt int) time.Duration {
	if len(d.cfg.BackoffSec) == 0 {
		return time.Second
	}
	i := attempt - 1
	if i >= len(d.cfg.BackoffSec) {
		i = len(d.cfg.BackoffSec) - 1
	}
	return time.Duration(d.cfg.BackoffSec[i]) * time.Second
}

// post выполняет один POST на URL подписки.
//
// Тело ответа читается и уезжает в журнал: без него в ленте видно только
// «стенд ответил 200», а что именно он вернул — нет. Ошибка чтения
// возвращается отдельно от результата: статус к этому моменту уже получен, и
// терять его нельзя — «стенд ответил 200, но связь оборвалась на теле» не то
// же самое, что «стенд не ответил вовсе».
func (d *Dispatcher) post(sub store.Subscription, body []byte) (postResult, error) {
	start := time.Now()
	req, err := http.NewRequest(http.MethodPost, sub.URL, bytes.NewReader(body))
	if err != nil {
		return postResult{Duration: time.Since(start)}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	if sub.Secret != "" {
		req.Header.Set("X-Max-Bot-Api-Secret", sub.Secret)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return postResult{Duration: time.Since(start)}, err
	}
	defer resp.Body.Close()

	// Читаем на байт больше потолка: иначе тело ровно в потолок неотличимо от
	// обрезанного, и на нём появилась бы ложная пометка.
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody+1))
	res := postResult{Status: resp.StatusCode, Body: string(raw), Duration: time.Since(start)}
	if len(raw) > maxResponseBody {
		res.Body = fmt.Sprintf("%s…(обрезано, показаны первые %d Б)", raw[:maxResponseBody], maxResponseBody)
	}
	return res, readErr
}

// Wait блокируется, пока все поставленные в очередь события не доставлены.
// Нужен тестам и корректной остановке сервиса.
func (d *Dispatcher) Wait() { d.wg.Wait() }

// Close дожидается доставки всего, что уже в очередях, и останавливает
// обработчиков.
func (d *Dispatcher) Close() {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.closed = true
	queues := make([]chan job, 0, len(d.queues))
	for _, q := range d.queues {
		queues = append(queues, q)
	}
	d.queues = make(map[int64]chan job)
	d.mu.Unlock()

	d.wg.Wait()
	for _, q := range queues {
		close(q)
	}
	d.work.Wait()
}
