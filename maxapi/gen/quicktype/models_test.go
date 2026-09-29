package maxapi

import (
	"encoding/json"
	"reflect"
	"testing"
)

// Тело, записанное с живого Max: message_created, которым пользователь ответил
// на кнопку request_contact. Источник и разбор полей —
// maxmoc/internal/core/live_sample_test.go. Проверяем на нём именно то, что
// генератор мог сломать молча: разрядность seq, указатели у nullable-полей и
// разбор полиморфного вложения.
const liveMessageCreated = `{
  "update_type": "message_created",
  "timestamp": 1786079712219,
  "user_locale": "ru",
  "message": {
    "recipient": {"chat_id": 442209522, "chat_type": "dialog", "user_id": 271648516},
    "timestamp": 1786079712219,
    "body": {
      "mid": "mid.000000001a5b94f2019fdaa593db1d43",
      "seq": 117052520019991875,
      "text": "",
      "attachments": [
        {"type": "contact", "payload": {"vcf_info": "BEGIN:VCARD\nEND:VCARD", "max_info": {
          "user_id": 174756854, "first_name": "Артём", "is_bot": false,
          "last_activity_time": 1786079712000, "name": "Артём"}}}
      ]
    },
    "sender": {
      "user_id": 174756854, "first_name": "Артём", "last_name": "",
      "is_bot": false, "last_activity_time": 1786079712000, "name": "Артём"
    }
  }
}`

func TestMessageCreatedUpdateПарситЖивоеТело(t *testing.T) {
	var got MessageCreatedUpdate
	if err := json.Unmarshal([]byte(liveMessageCreated), &got); err != nil {
		t.Fatalf("разбор тела: %v", err)
	}

	if got.UpdateType != PurpleMessageCreated {
		t.Errorf("update_type = %q, ожидался %q", got.UpdateType, PurpleMessageCreated)
	}
	// seq вне JSON-safe диапазона 2^53-1 — именно ради него поле объявлено
	// без @maxValue. Если генератор выдаст float64 или int32, тест упадёт.
	if want := int64(117052520019991875); got.Message.Body.Seq != want {
		t.Errorf("seq = %d, ожидалось %d", got.Message.Body.Seq, want)
	}
	if got.Message.Body.Mid != "mid.000000001a5b94f2019fdaa593db1d43" {
		t.Errorf("mid = %q", got.Message.Body.Mid)
	}
	if got.Message.Recipient.ChatID == nil || *got.Message.Recipient.ChatID != 442209522 {
		t.Errorf("chat_id = %v", got.Message.Recipient.ChatID)
	}
	if got.Message.Sender == nil || got.Message.Sender.Name == nil || *got.Message.Sender.Name != "Артём" {
		t.Errorf("sender = %+v", got.Message.Sender)
	}
	if got.UserLocale == nil || *got.UserLocale != "ru" {
		t.Errorf("user_locale = %v", got.UserLocale)
	}
}

func TestВложениеРазбираетсяЧерезПлоскийPayload(t *testing.T) {
	var got MessageCreatedUpdate
	if err := json.Unmarshal([]byte(liveMessageCreated), &got); err != nil {
		t.Fatal(err)
	}

	attachments := got.Message.Body.Attachments
	if len(attachments) != 1 {
		t.Fatalf("вложений: %d, ожидалось 1", len(attachments))
	}
	if attachments[0].Type != PurpleContact {
		t.Errorf("type вложения = %q", attachments[0].Type)
	}
	// Payload — плоское объединение всех девяти типов вложений: поля читаются
	// по дискриминатору type, чужие остаются nil.
	payload := attachments[0].Payload
	if payload == nil {
		t.Fatal("payload вложения nil")
	}
	if payload.VcfInfo == nil || *payload.VcfInfo == "" {
		t.Errorf("vcf_info = %v", payload.VcfInfo)
	}
	if payload.MaxInfo == nil || payload.MaxInfo.UserID != 174756854 {
		t.Errorf("max_info = %+v", payload.MaxInfo)
	}
	if payload.PhotoID != nil {
		t.Errorf("photo_id должен быть nil у contact-вложения, получено %v", *payload.PhotoID)
	}
}

func TestПлоскийUpdateДиспетчеризуетсяПоUpdateType(t *testing.T) {
	// Так вебхук-сервер и будет читать тело: один разбор, затем switch.
	var got Update
	if err := json.Unmarshal([]byte(liveMessageCreated), &got); err != nil {
		t.Fatalf("разбор тела: %v", err)
	}
	if string(got.UpdateType) != "message_created" {
		t.Fatalf("update_type = %q", got.UpdateType)
	}
	if got.Message == nil {
		t.Fatal("message nil у message_created")
	}
	if got.Message.Body.Seq != 117052520019991875 {
		t.Errorf("seq = %d", got.Message.Body.Seq)
	}
	// Поля прочих вариантов остаются пустыми.
	if got.Callback != nil || got.ChatID != nil {
		t.Errorf("поля чужих вариантов заполнены: callback=%v chat_id=%v",
			got.Callback, got.ChatID)
	}
}

func TestUpdateUnifiedЧитаетТоЖеТело(t *testing.T) {
	var got UpdateUnified
	if err := json.Unmarshal([]byte(liveMessageCreated), &got); err != nil {
		t.Fatalf("разбор тела: %v", err)
	}
	if got.UpdateType != "message_created" {
		t.Errorf("update_type = %q", got.UpdateType)
	}
	if got.Message == nil || got.Message.Body.Mid == "" {
		t.Errorf("message = %+v", got.Message)
	}
}

func TestRoundTripСохраняетЗначения(t *testing.T) {
	var first MessageCreatedUpdate
	if err := json.Unmarshal([]byte(liveMessageCreated), &first); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("сериализация: %v", err)
	}
	var second MessageCreatedUpdate
	if err := json.Unmarshal(encoded, &second); err != nil {
		t.Fatalf("повторный разбор: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Errorf("round-trip изменил значение:\n до: %+v\nпосле: %+v", first, second)
	}
}

func TestNullableПоляСтановятсяУказателями(t *testing.T) {
	// text объявлен nullable: пустая строка и null должны различаться.
	var withEmpty MessageCreatedUpdate
	if err := json.Unmarshal([]byte(liveMessageCreated), &withEmpty); err != nil {
		t.Fatal(err)
	}
	if withEmpty.Message.Body.Text == nil {
		t.Fatal(`text: "" разобрался в nil, ожидался указатель на ""`)
	}
	if *withEmpty.Message.Body.Text != "" {
		t.Errorf("text = %q, ожидалась пустая строка", *withEmpty.Message.Body.Text)
	}

	nullText := `{"update_type":"message_created","timestamp":1,"message":{
		"recipient":{"chat_id":1,"chat_type":"dialog","user_id":2},
		"timestamp":1,"body":{"mid":"mid.1","seq":1,"text":null}}}`
	var withNull MessageCreatedUpdate
	if err := json.Unmarshal([]byte(nullText), &withNull); err != nil {
		t.Fatal(err)
	}
	if withNull.Message.Body.Text != nil {
		t.Errorf("text: null разобрался в %q, ожидался nil", *withNull.Message.Body.Text)
	}
}
