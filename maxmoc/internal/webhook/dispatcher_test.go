package webhook

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"maxmock/internal/config"
	"maxmock/internal/events"
	"maxmock/internal/specs"
	"maxmock/internal/store"
	"maxmock/internal/wire"
)

const nowMS = int64(1722800000000)

type fixture struct {
	store *store.Store
	disp  *Dispatcher
	bus   *events.Bus
	bot   *store.Bot
	chat  int64
}

func newFixture(t *testing.T, cfg config.Webhook) *fixture {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "wh.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	sp, err := specs.Load()
	if err != nil {
		t.Fatal(err)
	}
	bot, err := st.CreateBot("Бот", "bot", "")
	if err != nil {
		t.Fatal(err)
	}
	_, dialog, err := st.CreateClient(bot.ID, store.ClientInput{FirstName: "Клиент", Username: "client"})
	if err != nil {
		t.Fatal(err)
	}
	bus := events.New()
	d, err := New(st, sp, bus, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	return &fixture{store: st, disp: d, bus: bus, bot: bot, chat: dialog.ChatID}
}

func removedUpdate(chatID int64, mid string) wire.MessageRemovedUpdate {
	return wire.MessageRemovedUpdate{
		UpdateBase: wire.UpdateBase{UpdateType: wire.UpdateMessageRemoved, Timestamp: nowMS},
		MessageID:  mid, ChatID: chatID, UserID: 42,
	}
}

func fastCfg() config.Webhook {
	return config.Webhook{TimeoutSec: 2, Retries: 2, BackoffSec: []int{0}}
}

func TestDeliversWithSecretHeader(t *testing.T) {
	f := newFixture(t, fastCfg())

	var (
		mu     sync.Mutex
		bodies [][]byte
		secret string
		ctype  string
		ua     string
	)
	stand := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, b)
		secret = r.Header.Get("X-Max-Bot-Api-Secret")
		ctype = r.Header.Get("Content-Type")
		ua = r.Header.Get("User-Agent")
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer stand.Close()

	if _, err := f.store.AddSubscription(f.bot.ID, stand.URL, nil, "секретное-слово"); err != nil {
		t.Fatal(err)
	}
	if err := f.disp.Deliver(f.bot.ID, f.chat, wire.UpdateMessageRemoved, removedUpdate(f.chat, "mid.1")); err != nil {
		t.Fatal(err)
	}
	f.disp.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 1 {
		t.Fatalf("доставок: %d", len(bodies))
	}
	if secret != "секретное-слово" {
		t.Errorf("заголовок секрета: %q", secret)
	}
	if ctype != "application/json" {
		t.Errorf("Content-Type: %q", ctype)
	}
	if ua != "OneMe/0.1.10 Bot API" {
		t.Errorf("User-Agent: %q, а живой MAX шлёт OneMe/0.1.10 Bot API", ua)
	}
	var got map[string]any
	if err := json.Unmarshal(bodies[0], &got); err != nil {
		t.Fatal(err)
	}
	if got["update_type"] != wire.UpdateMessageRemoved || got["message_id"] != "mid.1" {
		t.Errorf("тело доставки: %v", got)
	}

	deliveries, err := f.store.ListDeliveries(f.bot.ID, 10)
	if err != nil || len(deliveries) != 1 {
		t.Fatalf("журнал доставок: %d, %v", len(deliveries), err)
	}
	if deliveries[0].Status != 200 || deliveries[0].Attempt != 1 {
		t.Errorf("запись журнала: %+v", deliveries[0])
	}
}

func TestNoSecretHeaderWhenNotConfigured(t *testing.T) {
	f := newFixture(t, fastCfg())
	got := make(chan string, 1)
	stand := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("X-Max-Bot-Api-Secret")
		w.WriteHeader(http.StatusOK)
	}))
	defer stand.Close()

	_, _ = f.store.AddSubscription(f.bot.ID, stand.URL, nil, "")
	_ = f.disp.Deliver(f.bot.ID, f.chat, wire.UpdateMessageRemoved, removedUpdate(f.chat, "mid.1"))
	f.disp.Wait()

	if h := <-got; h != "" {
		t.Errorf("заголовок секрета не должен ставиться: %q", h)
	}
}

func TestUpdateTypesFilter(t *testing.T) {
	f := newFixture(t, fastCfg())
	var hits atomic.Int32
	stand := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer stand.Close()

	_, _ = f.store.AddSubscription(f.bot.ID, stand.URL, []string{wire.UpdateMessageCreated}, "")
	_ = f.disp.Deliver(f.bot.ID, f.chat, wire.UpdateMessageRemoved, removedUpdate(f.chat, "mid.1"))
	f.disp.Wait()

	if n := hits.Load(); n != 0 {
		t.Fatalf("событие вне фильтра доставлено %d раз", n)
	}
	// Отфильтрованное событие на стенд не уходит, но в журнале обязано
	// остаться — иначе оно неотличимо от «мок не отправил».
	d, _ := f.store.ListDeliveries(f.bot.ID, 10)
	if len(d) != 1 || !strings.Contains(d[0].Error, "нет подписки") {
		t.Errorf("отфильтрованное событие не зафиксировано в журнале: %+v", d)
	}
}

func TestRetriesOnServerError(t *testing.T) {
	f := newFixture(t, fastCfg())
	var attempts atomic.Int32
	stand := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer stand.Close()

	_, _ = f.store.AddSubscription(f.bot.ID, stand.URL, nil, "")
	_ = f.disp.Deliver(f.bot.ID, f.chat, wire.UpdateMessageRemoved, removedUpdate(f.chat, "mid.1"))
	f.disp.Wait()

	if n := attempts.Load(); n != 3 {
		t.Fatalf("попыток: %d, ожидалось 3", n)
	}
	deliveries, _ := f.store.ListDeliveries(f.bot.ID, 10)
	if len(deliveries) != 3 {
		t.Fatalf("в журнале %d попыток, ожидалось 3", len(deliveries))
	}
	if deliveries[0].Status != 200 {
		t.Errorf("последняя попытка должна быть успешной: %+v", deliveries[0])
	}
}

// Тело ответа стенда обязано попадать в журнал: в ленте диалога это вторая
// половина обмена «max → бот», и без неё видно только «стенд ответил 200».
func TestResponseBodyIsJournalled(t *testing.T) {
	f := newFixture(t, fastCfg())
	stand := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"accepted":true}`))
	}))
	defer stand.Close()

	_, _ = f.store.AddSubscription(f.bot.ID, stand.URL, nil, "")
	_ = f.disp.Deliver(f.bot.ID, f.chat, wire.UpdateMessageRemoved, removedUpdate(f.chat, "mid.1"))
	f.disp.Wait()

	entries, _ := f.store.ListDeliveries(f.bot.ID, 10)
	if len(entries) != 1 {
		t.Fatalf("записей о доставке: %d", len(entries))
	}
	if entries[0].ResponseBody != `{"accepted":true}` {
		t.Errorf("тело ответа стенда: %q", entries[0].ResponseBody)
	}
}

// Упавший стенд отвечает HTML-страницей ошибки; без потолка она осела бы в
// базе на каждой попытке каждого события.
func TestOversizedResponseBodyIsTruncated(t *testing.T) {
	f := newFixture(t, fastCfg())
	huge := strings.Repeat("a", maxResponseBody*2)
	stand := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(huge))
	}))
	defer stand.Close()

	_, _ = f.store.AddSubscription(f.bot.ID, stand.URL, nil, "")
	_ = f.disp.Deliver(f.bot.ID, f.chat, wire.UpdateMessageRemoved, removedUpdate(f.chat, "mid.1"))
	f.disp.Wait()

	entries, _ := f.store.ListDeliveries(f.bot.ID, 10)
	if len(entries) != 1 {
		t.Fatalf("записей о доставке: %d", len(entries))
	}
	got := entries[0].ResponseBody
	if len(got) >= len(huge) {
		t.Fatalf("тело не обрезано: %d Б", len(got))
	}
	if !strings.HasPrefix(got, strings.Repeat("a", maxResponseBody)) {
		t.Error("обрезано не с начала тела")
	}
	if !strings.Contains(got, "обрезано") {
		t.Errorf("обрезка не помечена: %q", got[maxResponseBody:])
	}
}

// Обрыв связи на чтении тела не должен превращать полученный 200 в «ответа не
// было»: стенд событие принял, и повторять доставку нельзя — она
// продублируется. Ошибка при этом обязана остаться в журнале.
func TestReadErrorKeepsStatusAndSkipsRetry(t *testing.T) {
	f := newFixture(t, fastCfg())
	var attempts atomic.Int32
	stand := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		// Соединение перехватывается, а не рвётся паникой: паника закрывает
		// его до сброса буфера, и клиент не видит даже заголовков — вышел бы
		// обычный сетевой сбой, а проверить надо обрыв именно на теле.
		conn, buf, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Errorf("перехват соединения: %v", err)
			return
		}
		defer conn.Close()
		// В Content-Length обещано больше, чем отдано: клиент получит статус,
		// начало тела и ошибку чтения на закрытии.
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 1000\r\n\r\nчастичный")
		_ = buf.Flush()
	}))
	defer stand.Close()

	_, _ = f.store.AddSubscription(f.bot.ID, stand.URL, nil, "")
	_ = f.disp.Deliver(f.bot.ID, f.chat, wire.UpdateMessageRemoved, removedUpdate(f.chat, "mid.1"))
	f.disp.Wait()

	if n := attempts.Load(); n != 1 {
		t.Fatalf("попыток: %d, ожидалась одна — стенд ответил 200", n)
	}
	entries, _ := f.store.ListDeliveries(f.bot.ID, 10)
	if len(entries) != 1 {
		t.Fatalf("записей о доставке: %d", len(entries))
	}
	e := entries[0]
	if e.Status != 200 {
		t.Errorf("статус потерян из-за обрыва на теле: %d", e.Status)
	}
	if e.Error == "" {
		t.Error("причина обрыва не записана")
	}
	if e.ResponseBody != "частичный" {
		t.Errorf("прочитанная часть тела потеряна: %q", e.ResponseBody)
	}
}

func TestNoRetryOnClientError(t *testing.T) {
	f := newFixture(t, fastCfg())
	var attempts atomic.Int32
	stand := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer stand.Close()

	_, _ = f.store.AddSubscription(f.bot.ID, stand.URL, nil, "")
	_ = f.disp.Deliver(f.bot.ID, f.chat, wire.UpdateMessageRemoved, removedUpdate(f.chat, "mid.1"))
	f.disp.Wait()

	if n := attempts.Load(); n != 1 {
		t.Fatalf("4xx повторять не нужно, попыток: %d", n)
	}
}

// Событие, не соответствующее webhook-контракту, не должно уходить на стенд
// ни при каких условиях — это ошибка мока, а не стенда.
func TestInvalidUpdateIsNotDelivered(t *testing.T) {
	f := newFixture(t, fastCfg())
	var hits atomic.Int32
	stand := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer stand.Close()

	_, _ = f.store.AddSubscription(f.bot.ID, stand.URL, nil, "")

	broken := map[string]any{"update_type": "message_removed", "timestamp": "не число"}
	err := f.disp.Deliver(f.bot.ID, f.chat, wire.UpdateMessageRemoved, broken)
	if err == nil {
		t.Fatal("невалидное событие принято к доставке")
	}
	f.disp.Wait()

	if n := hits.Load(); n != 0 {
		t.Errorf("невалидное событие ушло на стенд %d раз", n)
	}
	deliveries, _ := f.store.ListDeliveries(f.bot.ID, 10)
	if len(deliveries) != 1 || deliveries[0].Status != 0 || deliveries[0].Error == "" {
		t.Errorf("отказ не зафиксирован в журнале: %+v", deliveries)
	}
}

// В пределах диалога порядок событий обязан сохраняться.
func TestPerChatOrdering(t *testing.T) {
	f := newFixture(t, fastCfg())
	var (
		mu    sync.Mutex
		order []string
	)
	stand := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var u struct {
			MessageID string `json:"message_id"`
		}
		_ = json.Unmarshal(b, &u)
		mu.Lock()
		order = append(order, u.MessageID)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer stand.Close()

	_, _ = f.store.AddSubscription(f.bot.ID, stand.URL, nil, "")
	want := []string{"mid.1", "mid.2", "mid.3", "mid.4", "mid.5"}
	for _, mid := range want {
		if err := f.disp.Deliver(f.bot.ID, f.chat, wire.UpdateMessageRemoved, removedUpdate(f.chat, mid)); err != nil {
			t.Fatal(err)
		}
	}
	f.disp.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(order) != len(want) {
		t.Fatalf("доставлено %d из %d", len(order), len(want))
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("порядок нарушен: %v", order)
		}
	}
}

func TestDeliversToAllMatchingSubscriptions(t *testing.T) {
	f := newFixture(t, fastCfg())
	var a, b atomic.Int32
	standA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		a.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer standA.Close()
	standB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		b.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer standB.Close()

	_, _ = f.store.AddSubscription(f.bot.ID, standA.URL, nil, "")
	_, _ = f.store.AddSubscription(f.bot.ID, standB.URL, nil, "")
	_ = f.disp.Deliver(f.bot.ID, f.chat, wire.UpdateMessageRemoved, removedUpdate(f.chat, "mid.1"))
	f.disp.Wait()

	if a.Load() != 1 || b.Load() != 1 {
		t.Errorf("оба стенда должны получить событие: A=%d B=%d", a.Load(), b.Load())
	}
}

func TestDeliveryPublishedToBus(t *testing.T) {
	f := newFixture(t, fastCfg())
	ch, cancel := f.bus.Subscribe(f.bot.ID)
	defer cancel()

	stand := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer stand.Close()
	_, _ = f.store.AddSubscription(f.bot.ID, stand.URL, nil, "")
	_ = f.disp.Deliver(f.bot.ID, f.chat, wire.UpdateMessageRemoved, removedUpdate(f.chat, "mid.1"))
	f.disp.Wait()

	select {
	case ev := <-ch:
		if ev.Kind != events.KindDelivery {
			t.Errorf("вид события: %s", ev.Kind)
		}
	default:
		t.Error("доставка не опубликована в шину")
	}
}

// Подписок нет вовсе — не только тип не совпал (это отдельный случай в
// TestSkippedEventIsLogged), но и заведённой подписки на бота не существует.
// Deliver() отсутствием ошибки этого не покажет: она сигнализирует лишь о
// невалидном теле или остановленном диспетчере, а недоставка — асинхронный
// факт, живущий в журнале. Раньше эта ветка была прикрыта только медленным
// сквозным тестом; здесь она проверяется напрямую.
func TestNoSubscriptionsIsNotAnError(t *testing.T) {
	f := newFixture(t, fastCfg())
	if err := f.disp.Deliver(f.bot.ID, f.chat, wire.UpdateMessageRemoved, removedUpdate(f.chat, "mid.1")); err != nil {
		t.Fatalf("доставка без подписок должна проходить молча: %v", err)
	}
	f.disp.Wait()

	entries, err := f.store.ListDeliveries(f.bot.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("записей о недоставке: %d, ожидалась одна", len(entries))
	}
	e := entries[0]
	if e.URL != "" || e.Status != 0 {
		t.Errorf("недоставленное событие записано как отправленное: %+v", e)
	}
	if !strings.Contains(e.Error, "нет подписки") {
		t.Errorf("причина не записана: %q", e.Error)
	}
	if e.ChatID == nil || *e.ChatID != f.chat {
		t.Errorf("chat_id в записи о недоставке: %+v", e.ChatID)
	}
}

// TestSkippedEventIsLogged — событие, на тип которого стенд не подписан, не
// должно пропадать бесследно: иначе «мок не отправил» неотличимо от
// «стенд не подписан», и разбор упирается в тупик.
func TestSkippedEventIsLogged(t *testing.T) {
	f := newFixture(t, fastCfg())
	if _, err := f.store.AddSubscription(f.bot.ID, "https://stand.test/hook",
		[]string{"message_created"}, ""); err != nil {
		t.Fatal(err)
	}

	// bot_started, а не message_edited: тело простое, и тест проверяет
	// отсутствие подписки, а не форму события. Deliver валидирует тело по
	// контракту синхронно, поэтому лишние поля здесь только мешали бы.
	chatID := f.chat
	if err := f.disp.Deliver(f.bot.ID, chatID, "bot_started", map[string]any{
		"update_type": "bot_started",
		"timestamp":   int64(1),
		"chat_id":     chatID,
		"user": map[string]any{
			"user_id":            int64(5),
			"first_name":         "Иван",
			"username":           "ivan",
			"is_bot":             false,
			"last_activity_time": int64(1),
			"name":               nil,
		},
	}); err != nil {
		t.Fatal(err)
	}
	f.disp.Wait()

	entries, err := f.store.ListDeliveries(f.bot.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("записей о доставке: %d, ожидалась одна", len(entries))
	}
	e := entries[0]
	if e.URL != "" || e.Status != 0 {
		t.Errorf("недоставленное событие записано как отправленное: %+v", e)
	}
	if !strings.Contains(e.Error, "нет подписки") {
		t.Errorf("причина не записана: %q", e.Error)
	}
	if e.ChatID == nil || *e.ChatID != chatID {
		t.Errorf("chat_id в записи о недоставке: %+v", e.ChatID)
	}
}
