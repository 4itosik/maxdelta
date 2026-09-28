package specs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func load(t *testing.T) *Specs {
	t.Helper()
	s, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return s
}

func TestLoadReportsContractVersion(t *testing.T) {
	if v := load(t).Version(); v != "0.0.33" {
		t.Fatalf("версия контракта = %q, ожидалась 0.0.33", v)
	}
}

func TestFindRoute(t *testing.T) {
	s := load(t)
	for _, tc := range []struct{ method, path string }{
		{"GET", "/me"},
		{"POST", "/messages"},
		{"PUT", "/messages"},
		{"DELETE", "/messages"},
		{"GET", "/messages/mid.abc"},
		{"POST", "/answers"},
		{"GET", "/subscriptions"},
		{"POST", "/uploads"},
	} {
		r := httptest.NewRequest(tc.method, "http://localhost:8080"+tc.path, nil)
		if _, _, err := s.FindRoute(r); err != nil {
			t.Errorf("%s %s: %v", tc.method, tc.path, err)
		}
	}

	// Путей /chats в артефакте нет — они должны честно не находиться.
	r := httptest.NewRequest("GET", "http://localhost:8080/chats", nil)
	if _, _, err := s.FindRoute(r); err == nil {
		t.Error("GET /chats неожиданно найден в контракте")
	}
}

func postMessages(body string) *http.Request {
	r := httptest.NewRequest("POST", "http://localhost:8080/messages?user_id=42", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "x")
	return r
}

func TestValidateRequestBody(t *testing.T) {
	s := load(t)
	ctx := context.Background()

	ok := postMessages(`{"text":"привет","attachments":null,"link":null}`)
	route, params, err := s.FindRoute(ok)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateRequest(ctx, ok, route, params); err != nil {
		t.Fatalf("валидное тело отвергнуто: %v", err)
	}

	// text/attachments/link необязательны, как в официальной спецификации MAX:
	// тело из одного текста — пример PUT /messages из документации.
	short := postMessages(`{"text":"привет"}`)
	route, params, _ = s.FindRoute(short)
	if err := s.ValidateRequest(ctx, short, route, params); err != nil {
		t.Fatalf("тело без attachments/link отвергнуто: %v", err)
	}

	// Лишнее поле: NewMessageBody запечатан additionalProperties: false.
	bad := postMessages(`{"text":"привет","bogus":1}`)
	route, params, _ = s.FindRoute(bad)
	if err := s.ValidateRequest(ctx, bad, route, params); err == nil {
		t.Fatal("тело с лишним полем прошло валидацию")
	}

	// Неверный тип поля.
	bad2 := postMessages(`{"text":123,"attachments":null,"link":null}`)
	route, params, _ = s.FindRoute(bad2)
	if err := s.ValidateRequest(ctx, bad2, route, params); err == nil {
		t.Fatal("text числом прошёл валидацию")
	}
}

func TestValidateRequestKeepsBodyReadable(t *testing.T) {
	s := load(t)
	body := `{"text":"привет","attachments":null,"link":null}`
	r := postMessages(body)
	route, params, err := s.FindRoute(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateRequest(context.Background(), r, route, params); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(body))
	n, _ := r.Body.Read(buf)
	if string(buf[:n]) != body {
		t.Fatalf("тело после валидации нечитаемо: %q", string(buf[:n]))
	}
}

func TestValidateRequestQueryParams(t *testing.T) {
	s := load(t)
	ctx := context.Background()

	// type — обязательный query-параметр с enum.
	r := httptest.NewRequest("POST", "http://localhost:8080/uploads?type=image", nil)
	route, params, err := s.FindRoute(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateRequest(ctx, r, route, params); err != nil {
		t.Fatalf("type=image отвергнут: %v", err)
	}

	bad := httptest.NewRequest("POST", "http://localhost:8080/uploads?type=photo", nil)
	route, params, _ = s.FindRoute(bad)
	if err := s.ValidateRequest(ctx, bad, route, params); err == nil {
		t.Fatal("type=photo (значение вне enum) прошло валидацию")
	}

	missing := httptest.NewRequest("POST", "http://localhost:8080/uploads", nil)
	route, params, _ = s.FindRoute(missing)
	if err := s.ValidateRequest(ctx, missing, route, params); err == nil {
		t.Fatal("отсутствие обязательного type прошло валидацию")
	}
}

func TestValidateResponse(t *testing.T) {
	s := load(t)
	ctx := context.Background()
	r := httptest.NewRequest("GET", "http://localhost:8080/me", nil)
	route, params, err := s.FindRoute(r)
	if err != nil {
		t.Fatal(err)
	}
	h := http.Header{"Content-Type": []string{"application/json"}}

	good := []byte(`{"user_id":1,"first_name":"Бот","username":"bot","is_bot":true,` +
		`"last_activity_time":1722800000000,"name":null}`)
	if err := s.ValidateResponse(ctx, r, route, params, 200, h, good); err != nil {
		t.Fatalf("корректный BotInfo отвергнут: %v", err)
	}

	// Нет обязательного is_bot.
	bad := []byte(`{"user_id":1,"first_name":"Бот","username":"bot",` +
		`"last_activity_time":1722800000000,"name":null}`)
	if err := s.ValidateResponse(ctx, r, route, params, 200, h, bad); err == nil {
		t.Fatal("BotInfo без is_bot прошёл валидацию ответа")
	}

	// 400 в контракте Max не описан — такие ответы валидацию пропускают.
	if err := s.ValidateResponse(ctx, r, route, params, 400, h, []byte(`{"что":"угодно"}`)); err != nil {
		t.Fatalf("недокументированный статус не должен валидироваться: %v", err)
	}
}

// В 0.0.32 ChatType был enum ["chat"] и отвергал каждое событие диалога, а
// загрузчик расширял enum на лету. В 0.0.33 это строка с ограничениями по
// форме, костыля больше нет — проверяем, что оба реальных значения проходят,
// а произвольная строка по-прежнему нет.
func TestChatTypeDialogAccepted(t *testing.T) {
	s := load(t)
	ctx := context.Background()
	body := func(chatType string) []byte {
		return []byte(`{"update_type":"message_created","timestamp":1722800000000,"message":{` +
			`"recipient":{"chat_id":7,"chat_type":"` + chatType + `","user_id":42},` +
			`"timestamp":1722800000000,"body":{"mid":"mid.1","seq":1,"text":"привет"}}}`)
	}
	for _, ct := range []string{"dialog", "chat"} {
		if err := s.ValidateWebhookBody(ctx, body(ct)); err != nil {
			t.Errorf("chat_type=%q отвергнут: %v", ct, err)
		}
	}
	if err := s.ValidateWebhookBody(ctx, body("выдумка")); err == nil {
		t.Error("произвольный chat_type прошёл валидацию — enum не должен быть отключён")
	}
}

func TestValidateWebhookBody(t *testing.T) {
	s := load(t)
	ctx := context.Background()

	ok := []byte(`{"update_type":"message_removed","timestamp":1722800000000,` +
		`"message_id":"mid.0000000000000001","chat_id":7,"user_id":42}`)
	if err := s.ValidateWebhookBody(ctx, ok); err != nil {
		t.Fatalf("валидный message_removed отвергнут: %v", err)
	}

	started := []byte(`{"update_type":"bot_started","timestamp":1722800000000,"chat_id":7,` +
		`"user":{"user_id":42,"first_name":"Клиент","username":"client","is_bot":false,` +
		`"last_activity_time":1722800000000,"name":null},"user_locale":"ru"}`)
	if err := s.ValidateWebhookBody(ctx, started); err != nil {
		t.Fatalf("валидный bot_started отвергнут: %v", err)
	}

	for name, body := range map[string]string{
		"timestamp строкой": `{"update_type":"message_removed","timestamp":"не число","message_id":"m","chat_id":7,"user_id":42}`,
		"нет update_type":   `{"timestamp":1722800000000,"message_id":"m","chat_id":7,"user_id":42}`,
		"user не объект":    `{"update_type":"bot_started","timestamp":1722800000000,"chat_id":7,"user":5}`,
		"не JSON вообще":    `не json`,
	} {
		if err := s.ValidateWebhookBody(ctx, []byte(body)); err == nil {
			t.Errorf("%s: невалидное тело прошло валидацию", name)
		}
	}
}

// TestWebhookBodyCheckedAgainstStrictBranch закрепляет, что мок не полагается
// на снисходительную ветвь anyOf при проверке собственных событий.
//
// Тело операции вебхука описано как `anyOf: [UpdateUnified, WebhookUpdate]` —
// две формы на выбор разработчика бота. Плоская UpdateUnified требует только
// update_type и timestamp, а `anyOf` проходит при совпадении хотя бы одной
// ветви: без отдельной сверки со строгой ветвью через неё пролезает событие
// без обязательных полей своего типа, с неизвестным update_type и даже смесь
// полей от разных вариантов. Мок порождает события, а не принимает их, и
// обязан держать себя строже (см. ValidateWebhookBody).
func TestWebhookBodyCheckedAgainstStrictBranch(t *testing.T) {
	s := load(t)
	ctx := context.Background()

	const user = `{"user_id":42,"first_name":"Клиент","is_bot":false,` +
		`"last_activity_time":1722800000000,"name":"Клиент"}`

	ok := []byte(`{"update_type":"bot_started","timestamp":1722800000000,"chat_id":7,"user":` + user + `}`)
	if err := s.ValidateWebhookBody(ctx, ok); err != nil {
		t.Fatalf("валидный bot_started отвергнут: %v", err)
	}

	// Каждое тело удовлетворяет UpdateUnified и потому проходило бы anyOf.
	for name, body := range map[string]string{
		"bot_started без chat_id и user":        `{"update_type":"bot_started","timestamp":1722800000000}`,
		"message_created без message":           `{"update_type":"message_created","timestamp":1722800000000}`,
		"user_added без chat_id и is_channel":   `{"update_type":"user_added","timestamp":1722800000000}`,
		"chat_title_changed без title":          `{"update_type":"chat_title_changed","timestamp":1722800000000,"chat_id":7,"user":` + user + `}`,
		"update_type, которого нет в контракте": `{"update_type":"unknown_event","timestamp":1722800000000}`,
		"поля message_callback у bot_started": `{"update_type":"bot_started","timestamp":1722800000000,"chat_id":7,` +
			`"user":` + user + `,"callback":{"timestamp":1722800000000,"callback_id":"cb","user":` + user + `}}`,
	} {
		if err := s.ValidateWebhookBody(ctx, []byte(body)); err == nil {
			t.Errorf("%s: тело прошло валидацию — строгая ветвь не проверяется", name)
		}
	}
}

// Подписка на http:// — сознательное отступление мока от контракта ради
// закрытого контура (см. allowHTTPWebhookURL). Проверяется здесь, а не
// только в maxfacade: послабление наносится на сам документ, и «сломать» его
// можно, не трогая фасад — достаточно обновить спеку.
func TestSubscriptionAcceptsHTTPURL(t *testing.T) {
	s := load(t)
	ctx := context.Background()

	post := func(url string) error {
		r := httptest.NewRequest("POST", "http://localhost:8080/subscriptions",
			strings.NewReader(`{"url":"`+url+`"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "x")
		route, params, err := s.FindRoute(r)
		if err != nil {
			t.Fatal(err)
		}
		return s.ValidateRequest(ctx, r, route, params)
	}
	del := func(url string) error {
		r := httptest.NewRequest("DELETE", "http://localhost:8080/subscriptions?url="+url, nil)
		r.Header.Set("Authorization", "x")
		route, params, err := s.FindRoute(r)
		if err != nil {
			t.Fatal(err)
		}
		return s.ValidateRequest(ctx, r, route, params)
	}

	// Снять подписку должно быть можно ровно там, где её приняли: иначе
	// http-адрес остаётся в моке навсегда.
	for _, url := range []string{"https://stand.local/hook", "http://stand.local:8081/hook"} {
		if err := post(url); err != nil {
			t.Errorf("подписка на %s отвергнута: %v", url, err)
		}
		if err := del(url); err != nil {
			t.Errorf("отписка от %s отвергнута: %v", url, err)
		}
	}

	// Послабление касается только схемы: форма адреса проверяется по-прежнему.
	for _, url := range []string{"ftp://stand.local/hook", "stand.local/hook", "https://"} {
		if err := post(url); err == nil {
			t.Errorf("подписка на %q принята — проверка формы адреса потеряна", url)
		}
	}
}

// Послабление наносится на паттерн, а не отменяет его: если контракт
// перепишут, загрузчик обязан упасть, а не молча оставить мок строгим.
// Синтетический URL для проверки тела события собирается подстановкой
// значения в параметры пути (`/max/v1.0/webhooks/{integrationId}`). Если
// подставленное значение перестанет удовлетворять схеме параметра, событий
// не отправит ни одного — поэтому расхождение обязано всплывать на Load(),
// а не первым отвергнутым событием.
func TestWebhookSampleURLSatisfiesContract(t *testing.T) {
	s := load(t)
	if !strings.Contains(s.webhookURL, webhookPathParamSample) {
		t.Fatalf("URL = %q, ожидалась подстановка %q", s.webhookURL, webhookPathParamSample)
	}
	if err := s.checkWebhookURL(); err != nil {
		t.Fatalf("собранный URL не проходит контракт: %v", err)
	}

	bad := *s
	bad.webhookURL = "http://webhook.local/max/v1.0/webhooks/not-a-uuid"
	if err := bad.checkWebhookURL(); err == nil {
		t.Error("страж не заметил значения, не подходящего схеме параметра пути")
	}
}

func TestRelaxURLSchemeRejectsUnexpectedPattern(t *testing.T) {
	s := load(t)
	url := s.BotAPI.Components.Schemas["SubscriptionRequestBody"].Value.Properties["url"]
	if url.Value.Pattern != anyWebhookURL {
		t.Fatalf("pattern после загрузки = %q, ожидался %q", url.Value.Pattern, anyWebhookURL)
	}
	if err := relaxURLScheme("проверка", url); err == nil {
		t.Error("повторное послабление не заметило чужого паттерна")
	}
	if err := relaxURLScheme("проверка", nil); err == nil {
		t.Error("отсутствие поля не считается ошибкой")
	}
}
