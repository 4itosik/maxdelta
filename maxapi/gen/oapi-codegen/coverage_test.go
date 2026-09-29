package maxapi

import (
	"encoding/json"
	"testing"
)

// Проверка, ради которой webhook-документ не генерируется отдельно.
//
// openapi.MaxBotWebhook.yaml описывает тело вебхука как
// `anyOf: [UpdateUnified, WebhookUpdate]`, где WebhookUpdate — строгий oneOf
// из пятнадцати типов. Тот же oneOf из тех же пятнадцати есть в
// api-документе под именем Update (сверено: совпадает набор, отличается лишь
// порядок перечисления). Значит тип Update покрывает тело вебхука целиком, и
// генерация из второго документа дала бы 79 типов-близнецов в соседнем
// пакете, несовместимых с этими по типам.
//
// Утверждение проверяется, а не декларируется: ниже минимальное валидное тело
// каждого из пятнадцати типов — собрано из required-полей самой схемы
// скриптом, не руками, — и каждое должно разобраться в свой конкретный тип.
// Если в контракт добавят семнадцатый тип события, тест не заметит его сам,
// но список вариантов Update в схеме и здесь разъедется при первом же
// сравнении глазами; надёжнее — регенерировать этот файл.
func TestUpdateПокрываетВсеТипыСобытийВебхука(t *testing.T) {
	cases := []struct {
		schema     string
		updateType string
		body       string
		isVariant  func(any) bool
	}{
		{"MessageCreatedUpdate", "message_created", `{"update_type": "message_created", "message": {"recipient": {"chat_id": 1, "chat_type": "dialog", "user_id": 1}, "timestamp": 1, "body": {"mid": "mid.1", "seq": 1, "text": "x"}}, "timestamp": 1}`, func(v any) bool { _, ok := v.(MessageCreatedUpdate); return ok }},
		{"MessageCallbackUpdate", "message_callback", `{"update_type": "message_callback", "callback": {"timestamp": 1, "callback_id": "x", "user": {"user_id": 1, "first_name": "x", "is_bot": false, "last_activity_time": 1, "name": "x"}}, "message": {"recipient": {"chat_id": 1, "chat_type": "dialog", "user_id": 1}, "timestamp": 1, "body": {"mid": "mid.1", "seq": 1, "text": "x"}}, "timestamp": 1}`, func(v any) bool { _, ok := v.(MessageCallbackUpdate); return ok }},
		{"MessageEditedUpdate", "message_edited", `{"update_type": "message_edited", "message": {"recipient": {"chat_id": 1, "chat_type": "dialog", "user_id": 1}, "timestamp": 1, "body": {"mid": "mid.1", "seq": 1, "text": "x"}}, "timestamp": 1}`, func(v any) bool { _, ok := v.(MessageEditedUpdate); return ok }},
		{"MessageRemovedUpdate", "message_removed", `{"update_type": "message_removed", "message_id": "mid.1", "chat_id": 1, "user_id": 1, "timestamp": 1}`, func(v any) bool { _, ok := v.(MessageRemovedUpdate); return ok }},
		{"BotAddedToChatUpdate", "bot_added", `{"update_type": "bot_added", "chat_id": 1, "user": {"user_id": 1, "first_name": "x", "is_bot": false, "last_activity_time": 1, "name": "x"}, "is_channel": false, "timestamp": 1}`, func(v any) bool { _, ok := v.(BotAddedToChatUpdate); return ok }},
		{"BotRemovedFromChatUpdate", "bot_removed", `{"update_type": "bot_removed", "chat_id": 1, "user": {"user_id": 1, "first_name": "x", "is_bot": false, "last_activity_time": 1, "name": "x"}, "is_channel": false, "timestamp": 1}`, func(v any) bool { _, ok := v.(BotRemovedFromChatUpdate); return ok }},
		{"BotStartedUpdate", "bot_started", `{"update_type": "bot_started", "chat_id": 1, "user": {"user_id": 1, "first_name": "x", "is_bot": false, "last_activity_time": 1, "name": "x"}, "timestamp": 1}`, func(v any) bool { _, ok := v.(BotStartedUpdate); return ok }},
		{"BotStoppedUpdate", "bot_stopped", `{"update_type": "bot_stopped", "chat_id": 1, "user": {"user_id": 1, "first_name": "x", "is_bot": false, "last_activity_time": 1, "name": "x"}, "timestamp": 1}`, func(v any) bool { _, ok := v.(BotStoppedUpdate); return ok }},
		{"ChatTitleChangedUpdate", "chat_title_changed", `{"update_type": "chat_title_changed", "chat_id": 1, "user": {"user_id": 1, "first_name": "x", "is_bot": false, "last_activity_time": 1, "name": "x"}, "title": "x", "timestamp": 1}`, func(v any) bool { _, ok := v.(ChatTitleChangedUpdate); return ok }},
		{"DialogClearedUpdate", "dialog_cleared", `{"update_type": "dialog_cleared", "chat_id": 1, "user": {"user_id": 1, "first_name": "x", "is_bot": false, "last_activity_time": 1, "name": "x"}, "timestamp": 1}`, func(v any) bool { _, ok := v.(DialogClearedUpdate); return ok }},
		{"DialogMutedUpdate", "dialog_muted", `{"update_type": "dialog_muted", "chat_id": 1, "user": {"user_id": 1, "first_name": "x", "is_bot": false, "last_activity_time": 1, "name": "x"}, "muted_until": 1, "timestamp": 1}`, func(v any) bool { _, ok := v.(DialogMutedUpdate); return ok }},
		{"DialogUnmutedUpdate", "dialog_unmuted", `{"update_type": "dialog_unmuted", "chat_id": 1, "user": {"user_id": 1, "first_name": "x", "is_bot": false, "last_activity_time": 1, "name": "x"}, "timestamp": 1}`, func(v any) bool { _, ok := v.(DialogUnmutedUpdate); return ok }},
		{"DialogRemovedUpdate", "dialog_removed", `{"update_type": "dialog_removed", "chat_id": 1, "user": {"user_id": 1, "first_name": "x", "is_bot": false, "last_activity_time": 1, "name": "x"}, "timestamp": 1}`, func(v any) bool { _, ok := v.(DialogRemovedUpdate); return ok }},
		{"UserAddedToChatUpdate", "user_added", `{"update_type": "user_added", "chat_id": 1, "user": {"user_id": 1, "first_name": "x", "is_bot": false, "last_activity_time": 1, "name": "x"}, "is_channel": false, "timestamp": 1}`, func(v any) bool { _, ok := v.(UserAddedToChatUpdate); return ok }},
		{"UserRemovedFromChatUpdate", "user_removed", `{"update_type": "user_removed", "chat_id": 1, "user": {"user_id": 1, "first_name": "x", "is_bot": false, "last_activity_time": 1, "name": "x"}, "is_channel": false, "timestamp": 1}`, func(v any) bool { _, ok := v.(UserRemovedFromChatUpdate); return ok }},
	}

	if len(cases) != 15 {
		t.Fatalf("вариантов в тесте %d, в контракте 15", len(cases))
	}

	seen := map[string]bool{}
	for _, tc := range cases {
		t.Run(tc.schema, func(t *testing.T) {
			var u Update
			if err := json.Unmarshal([]byte(tc.body), &u); err != nil {
				t.Fatalf("разбор в Update: %v\nтело: %s", err, tc.body)
			}
			kind, err := u.Discriminator()
			if err != nil {
				t.Fatalf("Discriminator: %v", err)
			}
			if kind != tc.updateType {
				t.Errorf("update_type = %q, ожидался %q", kind, tc.updateType)
			}
			value, err := u.ValueByDiscriminator()
			if err != nil {
				t.Fatalf("ValueByDiscriminator: %v", err)
			}
			if !tc.isVariant(value) {
				t.Errorf("разобралось в %T, ожидался %s", value, tc.schema)
			}
		})
		seen[tc.updateType] = true
	}
	if len(seen) != 15 {
		t.Errorf("различных update_type: %d, ожидалось 15", len(seen))
	}
}
