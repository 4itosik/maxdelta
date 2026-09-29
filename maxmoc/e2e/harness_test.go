// Package e2e — сквозные тесты мока: поднимают сервис целиком и фейковый
// стенд КЦ, после чего проходят типовой сценарий интеграции.
//
// Валидация двусторонняя и жёсткая: каждый ответ мока сверяется с
// openapi.MaxBotApi.yaml, каждое пришедшее на стенд событие — с
// openapi.MaxBotWebhook.yaml. Тест падает не на «что-то пошло не так»,
// а на конкретном расхождении с контрактом.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"maxmock/internal/config"
	"maxmock/internal/core"
	"maxmock/internal/events"
	"maxmock/internal/httpserver"
	"maxmock/internal/specs"
	"maxmock/internal/store"
	"maxmock/internal/webhook"
)

const secret = "e2e-secret-1234"

// stand — фейковый стенд КЦ: принимает webhook-и, проверяет секрет и
// соответствие контракту, складывает события по типам.
// standAck — что стенд отвечает на вебхук. Живой стенд КЦ волен отвечать
// пустым 200; здесь тело непустое намеренно, чтобы было видно, доезжает ли
// оно до журнала.
const standAck = `{"accepted":true}`

type stand struct {
	t     *testing.T
	specs *specs.Specs
	srv   *httptest.Server

	mu       sync.Mutex
	received []map[string]any
	waiters  []waiter
}

type waiter struct {
	updateType string
	ch         chan map[string]any
}

func newStand(t *testing.T, sp *specs.Specs) *stand {
	s := &stand{t: t, specs: sp}
	// Стенд поднимается по TLS: контракт требует https у webhook-URL подписки
	// (SubscriptionRequestBody.url, pattern ^https://.+$), и сквозной тест
	// идёт этим — основным — путём. Сертификат самоподписанный, мок доверяет
	// ему через Webhook.InsecureSkipVerify (см. newMock).
	//
	// Подписка на http:// тоже принимается — послабление ради закрытого
	// контура (specs.allowHTTPWebhookURL), — но проверяется отдельно, в
	// maxfacade и specs: здесь важнее покрыть доставку на https-стенд.
	s.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("стенд не смог прочитать тело: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if got := r.Header.Get("X-Max-Bot-Api-Secret"); got != secret {
			t.Errorf("секрет подписки не передан или неверен: %q", got)
		}
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("Content-Type доставки: %q", ct)
		}
		if ua := r.Header.Get("User-Agent"); !strings.HasPrefix(ua, "OneMe/") {
			t.Errorf("User-Agent доставки: %q, а живой MAX шлёт OneMe/<версия> Bot API", ua)
		}
		if err := s.specs.ValidateWebhookBody(context.Background(), body); err != nil {
			t.Errorf("событие не соответствует webhook-контракту: %v\nтело: %s", err, body)
		}

		var ev map[string]any
		if err := json.Unmarshal(body, &ev); err != nil {
			t.Errorf("событие не разобрано: %v", err)
		}
		s.push(ev)
		// Стенд отвечает телом, а не пустым 200: ответ уезжает в журнал
		// доставок и показывается в ленте второй половиной обмена — сквозной
		// тест обязан проверить, что он доезжает.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(standAck))
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *stand) push(ev map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.received = append(s.received, ev)
	kept := s.waiters[:0]
	for _, w := range s.waiters {
		if w.updateType == ev["update_type"] {
			w.ch <- ev
			continue
		}
		kept = append(kept, w)
	}
	s.waiters = kept
}

// await дожидается события нужного типа: уже пришедшего или следующего.
func (s *stand) await(t *testing.T, updateType string) map[string]any {
	t.Helper()
	s.mu.Lock()
	for _, ev := range s.received {
		if ev["update_type"] == updateType {
			s.mu.Unlock()
			return ev
		}
	}
	ch := make(chan map[string]any, 1)
	s.waiters = append(s.waiters, waiter{updateType: updateType, ch: ch})
	s.mu.Unlock()

	select {
	case ev := <-ch:
		return ev
	case <-time.After(5 * time.Second):
		t.Fatalf("событие %s не пришло на стенд за 5 секунд; получены: %v", updateType, s.types())
		return nil
	}
}

// refute убеждается, что событие такого типа НЕ приходило.
func (s *stand) refute(t *testing.T, updateType string) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ev := range s.received {
		if ev["update_type"] == updateType {
			t.Fatalf("неожиданное событие %s: %v", updateType, ev)
		}
	}
}

func (s *stand) types() []string {
	out := []string{}
	for _, ev := range s.received {
		out = append(out, ev["update_type"].(string))
	}
	return out
}

func (s *stand) count(updateType string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, ev := range s.received {
		if ev["update_type"] == updateType {
			n++
		}
	}
	return n
}

// mock — поднятый экземпляр сервиса.
type mock struct {
	srv   *httptest.Server
	specs *specs.Specs
	disp  *webhook.Dispatcher
	store *store.Store
}

func newMock(t *testing.T) *mock {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "e2e.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	sp, err := specs.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.BlobDir = filepath.Join(dir, "blobs")
	// InsecureSkipVerify — стенды в тестах поднимаются на самоподписанных
	// сертификатах httptest.NewTLSServer (см. newStand).
	cfg.Webhook = config.Webhook{TimeoutSec: 3, Retries: 1, BackoffSec: []int{0}, InsecureSkipVerify: true}
	bus := events.New()
	disp, err := webhook.New(st, sp, bus, cfg.Webhook)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(disp.Close)

	c := core.New(st, disp, bus, cfg)
	srv := httptest.NewServer(httpserver.New(c, sp, bus, cfg))
	t.Cleanup(srv.Close)
	cfg.PublicBaseURL = srv.URL

	return &mock{srv: srv, specs: sp, disp: disp, store: st}
}

// call выполняет запрос к моку и, если это операция Bot API, сверяет ответ
// с контрактом — так тест ловит расхождение даже там, где мок сам его
// пропустил бы.
func (m *mock) call(t *testing.T, method, path, body, token string) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, m.srv.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	m.assertContract(t, method, path, body, resp, raw)
	return resp.StatusCode, raw
}

// assertContract сверяет ответ с openapi.MaxBotApi.yaml.
func (m *mock) assertContract(t *testing.T, method, path, body string, resp *http.Response, raw []byte) {
	t.Helper()
	probe, err := http.NewRequest(method, m.srv.URL+path, bytes.NewReader([]byte(body)))
	if err != nil {
		return
	}
	probe.Header.Set("Content-Type", "application/json")
	route, params, err := m.specs.FindRoute(probe)
	if err != nil {
		return // служебный путь /mock/... — вне контракта Max
	}
	if err := m.specs.ValidateResponse(context.Background(), probe, route, params,
		resp.StatusCode, resp.Header, raw); err != nil {
		t.Errorf("ответ %s %s (%d) не соответствует контракту: %v\nтело: %s",
			method, path, resp.StatusCode, err, raw)
	}
}

func (m *mock) mustCall(t *testing.T, method, path, body, token string) []byte {
	t.Helper()
	status, raw := m.call(t, method, path, body, token)
	if status != http.StatusOK {
		t.Fatalf("%s %s: статус %d, тело %s", method, path, status, raw)
	}
	return raw
}

func decode[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("не разобрано: %v (%s)", err, raw)
	}
	return v
}
