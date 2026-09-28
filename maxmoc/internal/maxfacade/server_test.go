package maxfacade

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"maxmock/internal/config"
	"maxmock/internal/core"
	"maxmock/internal/events"
	"maxmock/internal/specs"
	"maxmock/internal/store"
	"maxmock/internal/webhook"
	"maxmock/internal/wire"
)

type fixture struct {
	srv    *httptest.Server
	store  *store.Store
	core   *core.Core
	disp   *webhook.Dispatcher
	bot    *store.Bot
	client *store.Client
	chatID int64
	cfg    *config.Config
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "facade.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	sp, err := specs.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Webhook = config.Webhook{TimeoutSec: 1, Retries: 0, BackoffSec: []int{0}}
	bus := events.New()
	disp, err := webhook.New(st, sp, bus, cfg.Webhook)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(disp.Close)
	c := core.New(st, disp, bus, cfg)

	bot, err := st.CreateBot("Бот поддержки", "support_bot", "демо")
	if err != nil {
		t.Fatal(err)
	}
	cl, d, err := c.CreateClient(bot.ID, store.ClientInput{FirstName: "Иван", Username: "ivan"})
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(New(c, sp, bus, cfg))
	t.Cleanup(srv.Close)
	cfg.PublicBaseURL = srv.URL
	return &fixture{srv: srv, store: st, core: c, disp: disp, bot: bot, client: cl, chatID: d.ChatID, cfg: cfg}
}

func (f *fixture) do(t *testing.T, method, path, body string) (*http.Response, []byte) {
	t.Helper()
	return f.doAuth(t, method, path, body, f.bot.Token)
}

func (f *fixture) doAuth(t *testing.T, method, path, body, auth string) (*http.Response, []byte) {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, f.srv.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 0, 4096)
	chunk := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(chunk)
		buf = append(buf, chunk[:n]...)
		if err != nil {
			break
		}
	}
	return resp, buf
}

// lastLoggedChatID возвращает chat_id самой свежей записи журнала бота.
// ListRequestLog отдаёт записи новыми первыми, так что достаточно первой —
// вызывать сразу после проверяемого запроса, пока в журнал не набежало
// лишнего.
func (f *fixture) lastLoggedChatID(t *testing.T) *int64 {
	t.Helper()
	entries, err := f.store.ListRequestLog(&f.bot.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("журнал запросов пуст")
	}
	return entries[0].ChatID
}

// myCommands читает список команд бота через GET /me.
func (f *fixture) myCommands(t *testing.T) []wire.BotCommand {
	t.Helper()
	resp, body := f.do(t, "GET", "/me", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /me: %d %s", resp.StatusCode, body)
	}
	var me wire.BotInfo
	if err := json.Unmarshal(body, &me); err != nil {
		t.Fatalf("GET /me не разобран: %v (%s)", err, body)
	}
	return me.Commands
}

func decodeError(t *testing.T, body []byte) wire.Error {
	t.Helper()
	var e wire.Error
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("тело ошибки не разобрано: %v (%s)", err, body)
	}
	if e.Code == "" || e.Message == "" {
		t.Errorf("схема Error требует code и message: %s", body)
	}
	return e
}

// Паритет авторизации с живым Max: токен передаётся голым в заголовке
// Authorization. Схема Bearer и query-параметр access_token отвергаются —
// контракт объявляет обе, но прод не принимает ни одной.
//
// Ожидаемые коды, статусы и тексты — замер прода 2026-08-06, а не домысел:
// docs/specs/2026-08-06-auth-parity-tz.md.
func TestAuthorizationParity(t *testing.T) {
	f := newFixture(t)
	token := f.bot.Token

	cases := []struct {
		name    string
		auth    string
		query   string
		status  int
		message string
	}{
		{"заголовка нет", "", "", http.StatusUnauthorized, "No access token"},
		{"пробелы вместо токена", "   ", "", http.StatusUnauthorized, "No access token"},
		{"Bearer с валидным токеном", "Bearer " + token, "", http.StatusUnauthorized, "Malformed access token"},
		{"Bearer с мусором", "Bearer abc", "", http.StatusUnauthorized, "Malformed access token"},
		{"схема в нижнем регистре", "bearer abc", "", http.StatusUnauthorized, "Invalid access_token"},
		{"Bearer без пробела", "Bearer", "", http.StatusUnauthorized, "Invalid access_token"},
		{"голый токен", token, "", http.StatusOK, ""},
		{"голый токен в пробелах", "  " + token + "  ", "", http.StatusOK, ""},
		{"заголовок побеждает query", token, "?access_token=мусор", http.StatusOK, ""},
		{"только query", "", "?access_token=" + token, http.StatusUnauthorized,
			"Query parameter access_token is deprecated, use Authorization header"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := f.doAuth(t, "GET", "/me"+tc.query, "", tc.auth)
			if resp.StatusCode != tc.status {
				t.Fatalf("статус %d, ожидался %d: %s", resp.StatusCode, tc.status, body)
			}
			if tc.status == http.StatusOK {
				return
			}
			e := decodeError(t, body)
			if e.Code != CodeVerifyToken {
				t.Errorf("код %q, ожидался %q", e.Code, CodeVerifyToken)
			}
			if e.Message != tc.message {
				t.Errorf("сообщение %q, ожидалось %q", e.Message, tc.message)
			}
		})
	}
}

// PATCH /me/commands — команды сохраняются, видны в GET /me, пустой список
// их удаляет, отсутствие поля ничего не меняет.
func TestEditMyCommands(t *testing.T) {
	f := newFixture(t)

	resp, body := f.do(t, "PATCH", "/me/commands",
		`{"commands":[{"name":"start","description":"начать диалог"},{"name":"help"}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("статус %d: %s", resp.StatusCode, body)
	}
	var info wire.BotCommandsInfo
	if err := json.Unmarshal(body, &info); err != nil {
		t.Fatal(err)
	}
	if len(info.Commands) != 2 || info.Commands[0].Name != "start" {
		t.Fatalf("ответ: %+v", info.Commands)
	}
	if info.Commands[0].Description == nil || *info.Commands[0].Description != "начать диалог" {
		t.Errorf("описание команды потеряно: %+v", info.Commands[0])
	}

	// Команды видны в GET /me — так их читает КЦ.
	// Каждый раз разбираем в свежую структуру: json.Unmarshal не обнуляет
	// поля, которых нет в теле, и переиспользование скрыло бы потерю поля.
	if got := len(f.myCommands(t)); got != 2 {
		t.Fatalf("GET /me отдал %d команд, ожидалось 2", got)
	}

	// Отсутствие поля ничего не меняет.
	f.do(t, "PATCH", "/me/commands", `{}`)
	if got := len(f.myCommands(t)); got != 2 {
		t.Errorf("PATCH без commands изменил список: %d команд", got)
	}

	// Пустой список удаляет все команды.
	resp, body = f.do(t, "PATCH", "/me/commands", `{"commands":[]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("очистка: %d %s", resp.StatusCode, body)
	}
	if got := len(f.myCommands(t)); got != 0 {
		t.Errorf("команды не удалены: осталось %d", got)
	}

	// Команды переживают перезапуск: они в базе, а не в памяти процесса.
	stored, err := f.store.BotByID(f.bot.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(stored.Commands) != "[]" {
		t.Errorf("в хранилище: %s", stored.Commands)
	}
}

// Ограничения контракта на команды должна отклонять валидация, а не домен.
func TestEditMyCommandsValidation(t *testing.T) {
	f := newFixture(t)
	for name, body := range map[string]string{
		"без обязательного name": `{"commands":[{"description":"нет имени"}]}`,
		"пустое имя":             `{"commands":[{"name":""}]}`,
		"имя длиннее 64":         `{"commands":[{"name":"` + strings.Repeat("я", 65) + `"}]}`,
		"команд больше 32":       `{"commands":[` + strings.TrimSuffix(strings.Repeat(`{"name":"c"},`, 33), ",") + `]}`,
	} {
		resp, raw := f.do(t, "PATCH", "/me/commands", body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: статус %d (%s)", name, resp.StatusCode, raw)
			continue
		}
		if code := decodeError(t, raw).Code; code != CodeValidation {
			t.Errorf("%s: код %s", name, code)
		}
	}
}

func TestGetMyInfo(t *testing.T) {
	f := newFixture(t)
	resp, body := f.do(t, "GET", "/me", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("статус %d: %s", resp.StatusCode, body)
	}
	var info wire.BotInfo
	if err := json.Unmarshal(body, &info); err != nil {
		t.Fatal(err)
	}
	if info.UserID != f.bot.UserID || !info.IsBot {
		t.Errorf("BotInfo: %+v", info)
	}
	if resp.Header.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type: %s", resp.Header.Get("Content-Type"))
	}
}

func TestUnknownPathIsNotImplemented(t *testing.T) {
	f := newFixture(t)
	for _, path := range []string{"/chats", "/chats/1/members", "/выдумка"} {
		resp, body := f.do(t, "GET", path, "")
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: статус %d", path, resp.StatusCode)
			continue
		}
		if code := decodeError(t, body).Code; code != CodeNotImplemented {
			t.Errorf("%s: код %s", path, code)
		}
	}
}

// Операции, описанные в контракте, но вне объёма мока, отвечают явным
// mock.not_implemented, а не «упало».
func TestDocumentedButUnimplementedOperations(t *testing.T) {
	f := newFixture(t)
	cases := []struct{ method, path, body string }{
		{"GET", "/updates", ""},
		{"GET", "/videos/abc123", ""},
	}
	for _, tc := range cases {
		resp, body := f.do(t, tc.method, tc.path, tc.body)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s %s: статус %d (%s)", tc.method, tc.path, resp.StatusCode, body)
			continue
		}
		if code := decodeError(t, body).Code; code != CodeNotImplemented {
			t.Errorf("%s %s: код %s", tc.method, tc.path, code)
		}
	}
}

func TestRequestValidation(t *testing.T) {
	f := newFixture(t)

	// Лишнее поле: NewMessageBody запечатан additionalProperties: false.
	resp, body := f.do(t, "POST", "/messages?user_id="+itoa(f.client.UserID), `{"text":"a","bogus":1}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("статус %d: %s", resp.StatusCode, body)
	}
	if code := decodeError(t, body).Code; code != CodeValidation {
		t.Errorf("код: %s", code)
	}

	// Значение вне enum.
	resp, body = f.do(t, "POST", "/uploads?type=photo", "")
	if resp.StatusCode != http.StatusBadRequest || decodeError(t, body).Code != CodeValidation {
		t.Errorf("type=photo: %d %s", resp.StatusCode, body)
	}

	// Отсутствует обязательный параметр.
	resp, _ = f.do(t, "PUT", "/messages", `{"text":"a","attachments":null,"link":null}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("без message_id: %d", resp.StatusCode)
	}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

func TestMessageLifecycle(t *testing.T) {
	f := newFixture(t)

	// Отправка.
	resp, body := f.do(t, "POST", "/messages?user_id="+itoa(f.client.UserID),
		`{"text":"здравствуйте","attachments":null,"link":null}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("отправка: %d %s", resp.StatusCode, body)
	}
	var sent wire.SendMessageResult
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatal(err)
	}
	mid := sent.Message.Body.Mid
	if mid == "" {
		t.Fatalf("mid пуст: %s", body)
	}

	// Чтение по идентификатору.
	resp, body = f.do(t, "GET", "/messages/"+mid, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("чтение: %d %s", resp.StatusCode, body)
	}
	// getMessageByID резолвит диалог по mid сам — noteChat вызывается уже
	// после того, как сообщение найдено в store, и без утверждения здесь
	// эта проводка ничем не отличима от отсутствующей.
	if got := f.lastLoggedChatID(t); got == nil || *got != f.chatID {
		t.Errorf("GET /messages/{messageId}: chat_id в журнале = %v, ожидался %d", got, f.chatID)
	}

	// Список по chat_id.
	resp, body = f.do(t, "GET", "/messages?chat_id="+itoa(f.chatID), "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("список: %d %s", resp.StatusCode, body)
	}
	var list wire.MessageList
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Messages) != 1 {
		t.Errorf("в списке %d сообщений", len(list.Messages))
	}
	// getMessages — единственная из шести точек noteChat, где диалог берётся
	// из фильтра запроса, а не из резолва сущности: проверить нужно оба его
	// состояния, а не только «есть chat_id».
	if got := f.lastLoggedChatID(t); got == nil || *got != f.chatID {
		t.Errorf("GET /messages?chat_id=…: chat_id в журнале = %v, ожидался %d", got, f.chatID)
	}

	// Список по message_ids — тот же getMessages, но без chat_id в запросе.
	// Именно этим GET /messages отличается от POST /messages, где диалог
	// известен всегда: noteChat вызывается условно (f.ChatID != nil), и
	// журнал обязан остаться пустым, а не унаследовать чужой диалог из
	// предыдущей записи.
	resp, body = f.do(t, "GET", "/messages?message_ids="+mid, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("список по message_ids: %d %s", resp.StatusCode, body)
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Messages) != 1 {
		t.Errorf("в списке по message_ids %d сообщений", len(list.Messages))
	}
	if got := f.lastLoggedChatID(t); got != nil {
		t.Errorf("GET /messages?message_ids=…: chat_id в журнале = %v, ожидался nil", *got)
	}

	// Правка.
	resp, body = f.do(t, "PUT", "/messages?message_id="+mid,
		`{"text":"добрый день","attachments":null,"link":null}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("правка: %d %s", resp.StatusCode, body)
	}
	var simple wire.SimpleQueryResult
	if err := json.Unmarshal(body, &simple); err != nil || !simple.Success {
		t.Errorf("ответ правки: %s", body)
	}
	// Правка ботом чужого сообщения — тот самый след, чьё исчезновение из
	// журналов и завело задачу chat_id; без утверждения регресс здесь
	// незаметен.
	if got := f.lastLoggedChatID(t); got == nil || *got != f.chatID {
		t.Errorf("PUT /messages: chat_id в журнале = %v, ожидался %d", got, f.chatID)
	}

	// Удаление.
	resp, body = f.do(t, "DELETE", "/messages?message_id="+mid, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("удаление: %d %s", resp.StatusCode, body)
	}
	if got := f.lastLoggedChatID(t); got == nil || *got != f.chatID {
		t.Errorf("DELETE /messages: chat_id в журнале = %v, ожидался %d", got, f.chatID)
	}

	// После удаления сообщение недоступно: 404 объявлен для этой операции.
	resp, body = f.do(t, "GET", "/messages/"+mid, "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("чтение удалённого: %d %s", resp.StatusCode, body)
	}
	if code := decodeError(t, body).Code; code != CodeMessageNotFound {
		t.Errorf("код: %s", code)
	}
}

// Для DELETE /messages контракт не объявляет 404 — «сообщение не найдено»
// отдаётся как 400 с кодом mock.message.not_found.
func TestUndocumentedStatusFallsBackTo400(t *testing.T) {
	f := newFixture(t)
	resp, body := f.do(t, "DELETE", "/messages?message_id=mid.000000000000000000000099", "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("статус %d: %s", resp.StatusCode, body)
	}
	if code := decodeError(t, body).Code; code != CodeMessageNotFound {
		t.Errorf("код: %s", code)
	}
}

func TestSendToUnknownRecipient(t *testing.T) {
	f := newFixture(t)
	resp, body := f.do(t, "POST", "/messages?user_id=999999999",
		`{"text":"привет","attachments":null,"link":null}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("статус %d: %s", resp.StatusCode, body)
	}
	if code := decodeError(t, body).Code; code != CodeChatNotFound {
		t.Errorf("код: %s", code)
	}
}

func TestSubscriptionsRoundTrip(t *testing.T) {
	f := newFixture(t)

	resp, body := f.do(t, "POST", "/subscriptions",
		`{"url":"https://stand.local/hook","update_types":["message_created"],"secret":"abcdef"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("подписка: %d %s", resp.StatusCode, body)
	}
	var res wire.SimpleQueryResult
	_ = json.Unmarshal(body, &res)
	if !res.Success {
		t.Errorf("подписка не удалась: %s", body)
	}

	resp, body = f.do(t, "GET", "/subscriptions", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("список подписок: %d %s", resp.StatusCode, body)
	}
	var subs wire.GetSubscriptionsResult
	if err := json.Unmarshal(body, &subs); err != nil {
		t.Fatal(err)
	}
	if len(subs.Subscriptions) != 1 || subs.Subscriptions[0].URL != "https://stand.local/hook" {
		t.Fatalf("подписки: %+v", subs.Subscriptions)
	}
	if subs.Subscriptions[0].Time == 0 {
		t.Error("время создания подписки не заполнено")
	}

	resp, _ = f.do(t, "DELETE", "/subscriptions?url=https://stand.local/hook", "")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("отписка: %d", resp.StatusCode)
	}
	_, body = f.do(t, "GET", "/subscriptions", "")
	_ = json.Unmarshal(body, &subs)
	if len(subs.Subscriptions) != 0 {
		t.Errorf("после отписки осталось: %+v", subs.Subscriptions)
	}
}

// Подписку на http:// мок принимает, хотя контракт требует https: стенды
// закрытого контура живут на открытом HTTP, и отказ сделал бы мок там
// неприменимым (см. specs.allowHTTPWebhookURL). Отступление помечается в
// поле message ответа, а снять такую подписку должно быть можно тем же
// DELETE — иначе адрес остаётся в моке навсегда.
func TestSubscriptionAcceptsHTTPURL(t *testing.T) {
	f := newFixture(t)
	const url = "http://stand.local:8081/hook"

	resp, body := f.do(t, "POST", "/subscriptions", `{"url":"`+url+`","secret":"abcdef"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("подписка на http:// отвергнута: %d %s", resp.StatusCode, body)
	}
	var res wire.SimpleQueryResult
	if err := json.Unmarshal(body, &res); err != nil {
		t.Fatal(err)
	}
	if !res.Success {
		t.Errorf("подписка не удалась: %s", body)
	}
	if !strings.Contains(res.Message, "http://") {
		t.Errorf("отступление от контракта не помечено в ответе: %q", res.Message)
	}

	resp, body = f.do(t, "DELETE", "/subscriptions?url="+url, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("отписка от http:// отвергнута: %d %s", resp.StatusCode, body)
	}

	// Послабление касается только схемы адреса.
	resp, body = f.do(t, "POST", "/subscriptions", `{"url":"ftp://stand.local/hook"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("подписка на ftp:// принята: %d %s", resp.StatusCode, body)
	}
	if code := decodeError(t, body).Code; code != CodeValidation {
		t.Errorf("код: %s", code)
	}
}

// Секрет подписки должен пройти валидацию по контракту: pattern запрещает
// пробелы и кириллицу.
func TestSubscriptionSecretPattern(t *testing.T) {
	f := newFixture(t)
	resp, body := f.do(t, "POST", "/subscriptions", `{"url":"https://stand/hook","secret":"плохой секрет"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("секрет вне pattern принят: %d %s", resp.StatusCode, body)
	}
	if code := decodeError(t, body).Code; code != CodeValidation {
		t.Errorf("код: %s", code)
	}
}

func TestUploadEndpoint(t *testing.T) {
	f := newFixture(t)
	resp, body := f.do(t, "POST", "/uploads?type=image", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("статус %d: %s", resp.StatusCode, body)
	}
	var endpoint wire.UploadEndpoint
	if err := json.Unmarshal(body, &endpoint); err != nil {
		t.Fatal(err)
	}
	if endpoint.Token == "" || !strings.Contains(endpoint.URL, "/mock/upload/") {
		t.Errorf("upload-эндпоинт: %+v", endpoint)
	}
}

func TestRequestsAreLogged(t *testing.T) {
	f := newFixture(t)
	f.do(t, "GET", "/me", "")
	f.doAuth(t, "GET", "/me", "", "")

	entries, err := f.store.ListRequestLog(nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("записей журнала: %d", len(entries))
	}
	// Новые первыми: сначала неавторизованный запрос.
	if entries[0].Status != http.StatusUnauthorized || entries[0].BotID != nil {
		t.Errorf("неавторизованный запрос в журнале: %+v", entries[0])
	}
	if entries[1].Status != http.StatusOK || entries[1].BotID == nil || *entries[1].BotID != f.bot.ID {
		t.Errorf("успешный запрос в журнале: %+v", entries[1])
	}
	if entries[1].ResponseBody == "" {
		t.Error("тело ответа не сохранено")
	}
}

func TestRequestLogCarriesChatID(t *testing.T) {
	f := newFixture(t)

	// Операция с диалогом: chat_id обязан оказаться в журнале.
	f.do(t, http.MethodPost, "/messages?chat_id="+strconv.FormatInt(f.chatID, 10),
		`{"text":"привет","attachments":null,"link":null}`)
	// Операция без диалога: chat_id обязан остаться пустым.
	f.do(t, http.MethodGet, "/me", "")

	entries, err := f.store.ListRequestLog(&f.bot.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]*store.RequestLogEntry{}
	for i := range entries {
		byPath[entries[i].Path] = &entries[i]
	}

	send := byPath["/messages"]
	if send == nil || send.ChatID == nil || *send.ChatID != f.chatID {
		t.Errorf("POST /messages без chat_id в журнале: %+v", send)
	}
	if me := byPath["/me"]; me == nil || me.ChatID != nil {
		t.Errorf("GET /me диалога не касается, chat_id должен быть пуст: %+v", me)
	}
}

func TestAnswerLogCarriesChatIDFromCallback(t *testing.T) {
	f := newFixture(t)
	if err := f.core.ClientStart(f.chatID, ""); err != nil {
		t.Fatal(err)
	}
	// Клавиатуру отправляем через сам фасад: тест фасада должен ходить его
	// собственной поверхностью, а не звать ядро в обход.
	_, sentBody := f.do(t, http.MethodPost,
		"/messages?chat_id="+strconv.FormatInt(f.chatID, 10),
		`{"text":"нажмите","attachments":[{"type":"inline_keyboard","payload":{"buttons":[[{"type":"callback","text":"Привет","payload":"hello"}]]}}],"link":null}`)
	var sent wire.SendMessageResult
	if err := json.Unmarshal(sentBody, &sent); err != nil {
		t.Fatalf("ответ POST /messages не разобран: %v; тело %s", err, sentBody)
	}
	if err := f.core.ClientPressButton(f.chatID, sent.Message.Body.Mid, "hello"); err != nil {
		t.Fatal(err)
	}
	cb, err := f.store.LastCallback(f.bot.ID)
	if err != nil {
		t.Fatal(err)
	}

	f.do(t, http.MethodPost, "/answers?callback_id="+cb.CallbackID, `{}`)

	entries, err := f.store.ListRequestLog(&f.bot.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Path != "/answers" {
			continue
		}
		if e.ChatID == nil || *e.ChatID != f.chatID {
			t.Fatalf("POST /answers: диалог не выведен из callback_id: %+v", e)
		}
		return
	}
	t.Fatal("вызов /answers не попал в журнал")
}

// Ответ, разошедшийся с контрактом, не должен уйти клиенту как валидный.
func TestResponseValidationCatchesContractDrift(t *testing.T) {
	f := newFixture(t)
	sp, err := specs.Load()
	if err != nil {
		t.Fatal(err)
	}
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route, params, err := sp.FindRoute(r)
		if err != nil {
			t.Fatal(err)
		}
		rec := newRecorder()
		// BotInfo без обязательного is_bot.
		writeJSON(rec, http.StatusOK, map[string]any{"user_id": 1, "first_name": "Бот"})
		srv := New(f.core, sp, events.New(), f.core.Config())
		srv.validateOwnResponse(r, route, params, rec, nil)
		rec.flush(w)
	}))
	defer broken.Close()

	req, _ := http.NewRequest("GET", broken.URL+"/me", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("несоответствующий контракту ответ отдан как %d", resp.StatusCode)
	}
}
