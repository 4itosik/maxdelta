// Package wire — структуры сообщений Max Bot API ровно в той форме, в какой
// они ходят по проводу.
//
// Главное правило пакета: `omitempty` ставится ТОЛЬКО на необязательные поля.
// В контракте Max часть полей объявлена одновременно обязательными и
// nullable (`text`, `attachments`, `link` в NewMessageBody; `chat_id` и
// `user_id` в Recipient; `name` в User), и для них разница между «пришёл null»
// и «поля нет» содержательна: null в `attachments` при PUT /messages означает
// «не трогать вложения», а пустой массив — «удалить все». Поэтому такие поля
// — указатели/срезы БЕЗ omitempty.
//
// Единственное исключение — `username` в User: там правило уступает
// наблюдаемому поведению прода, причины описаны у самого поля.
package wire

import "encoding/json"

// Типы событий, которые порождает мок.
const (
	UpdateMessageCreated  = "message_created"
	UpdateMessageCallback = "message_callback"
	UpdateMessageEdited   = "message_edited"
	UpdateMessageRemoved  = "message_removed"
	UpdateBotStarted      = "bot_started"
	UpdateBotStopped      = "bot_stopped"
)

// ChatTypeDialog — единственный тип чата, который эмулирует мок.
const ChatTypeDialog = "dialog"

// Типы вложений.
const (
	AttachmentImage          = "image"
	AttachmentVideo          = "video"
	AttachmentAudio          = "audio"
	AttachmentFile           = "file"
	AttachmentInlineKeyboard = "inline_keyboard"
	AttachmentReplyKeyboard  = "reply_keyboard"
	AttachmentShare          = "share"
	AttachmentLocation       = "location"
	AttachmentSticker        = "sticker"
	AttachmentContact        = "contact"
)

// Error — тело ответа об ошибке. Поле `error` контрактом разрешено, но мок
// его не заполняет: `code` и `message` несут всю информацию.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// SimpleQueryResult — ответ операций, не возвращающих данных.
type SimpleQueryResult struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
}

// BotCommand — команда, поддерживаемая ботом.
type BotCommand struct {
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
}

// BotCommandsInfo — ответ PATCH /me/commands.
type BotCommandsInfo struct {
	Commands []BotCommand `json:"commands"`
}

// BotCommandsPatch — тело PATCH /me/commands.
//
// Поле commands необязательно, и его отсутствие отличается от пустого списка:
// пустой список означает «удалить все команды» (так сказано в описании
// операции), отсутствие — «не трогать».
type BotCommandsPatch struct {
	Commands []BotCommand `json:"commands"`

	commandsSet bool
}

// CommandsSet сообщает, было ли поле commands в теле запроса.
func (p *BotCommandsPatch) CommandsSet() bool { return p.commandsSet }

// UnmarshalJSON разбирает тело, запоминая наличие поля commands.
func (p *BotCommandsPatch) UnmarshalJSON(data []byte) error {
	var raw struct {
		Commands *[]BotCommand `json:"commands"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	p.commandsSet = raw.Commands != nil
	if raw.Commands != nil {
		p.Commands = *raw.Commands
	}
	return nil
}

// User — пользователь или бот.
//
// `username` — исключение из правила пакета: контракт объявляет его
// обязательным, но живой Max у пользователя без публичного имени поле не
// присылает вовсе (см. образец в core/live_sample_test.go), то есть нарушает
// собственную спеку. Мок повторяет поведение прода, а не спеки: клиент, чья
// модель сгенерирована из контракта и требует username, должен спотыкаться на
// моке ровно там же, где споткнётся на проде. Ради этого `username` убран из
// required в обоих api/openapi.*.yaml — иначе диспетчер отвергал бы
// собственные события мока как не соответствующие контракту.
//
// `last_name` живой Max, наоборот, присылает всегда, даже пустой; у ботов
// поля нет — так его описывает и контракт.
type User struct {
	UserID           int64   `json:"user_id"`
	FirstName        string  `json:"first_name"`
	LastName         *string `json:"last_name,omitempty"`
	Username         *string `json:"username,omitempty"`
	IsBot            bool    `json:"is_bot"`
	LastActivityTime int64   `json:"last_activity_time"`
	Name             *string `json:"name"`
}

// BotInfo — ответ GET /me: User плюс аватар, описание и команды.
type BotInfo struct {
	User
	Description   *string      `json:"description,omitempty"`
	AvatarURL     string       `json:"avatar_url,omitempty"`
	FullAvatarURL string       `json:"full_avatar_url,omitempty"`
	Commands      []BotCommand `json:"commands,omitempty"`
}

// Recipient — получатель сообщения. chat_id и user_id обязательны и nullable.
type Recipient struct {
	ChatID   *int64 `json:"chat_id"`
	ChatType string `json:"chat_type"`
	UserID   *int64 `json:"user_id"`
}

// MarkupElement — элемент разметки текста.
type MarkupElement struct {
	Type   string `json:"type"`
	From   int32  `json:"from"`
	Length int32  `json:"length"`
	URL    string `json:"url,omitempty"`
	UserID *int64 `json:"user_id,omitempty"`
}

// Attachment — вложение сообщения. Контракт описывает вложения как
// дискриминируемое объединение одиннадцати типов; вместо генерации union-типа
// поля всех вариантов собраны в одну структуру, а `payload` остаётся сырым
// JSON — так его форму задаёт код, создающий вложение конкретного типа.
type Attachment struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`

	// file
	Filename string `json:"filename,omitempty"`
	Size     *int64 `json:"size,omitempty"`
	// video, sticker
	Width    *int `json:"width,omitempty"`
	Height   *int `json:"height,omitempty"`
	Duration *int `json:"duration,omitempty"`
	// location
	Latitude  *float64 `json:"latitude,omitempty"`
	Longitude *float64 `json:"longitude,omitempty"`
	// reply_keyboard
	Buttons [][]Button `json:"buttons,omitempty"`
	// share
	Title       *string `json:"title,omitempty"`
	Description *string `json:"description,omitempty"`
	ImageURL    *string `json:"image_url,omitempty"`
}

// ButtonRequestGeoLocation — единственный тип кнопки, который моку нужно
// различать поимённо: только у него в схеме есть поле со значением по
// умолчанию, и прод это умолчание материализует (см. withButtonDefaults).
// Остальные типы мок переносит из запроса в сообщение, не разбирая.
const ButtonRequestGeoLocation = "request_geo_location"

// Button — кнопка клавиатуры.
type Button struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Payload string `json:"payload,omitempty"`
	URL     string `json:"url,omitempty"`
	Quick   *bool  `json:"quick,omitempty"`
}

// Keyboard — полезная нагрузка вложения inline_keyboard.
type Keyboard struct {
	Buttons [][]Button `json:"buttons"`
}

// ContactPayload — полезная нагрузка вложения contact в **сообщении**.
//
// Форма отличается от формы запроса (см. ContactRequestPayload): контракт
// объявляет для них разные схемы, и путать их значит слать стенду вложение,
// которого живой Max не пришлёт.
type ContactPayload struct {
	VcfInfo *string `json:"vcf_info"`
	Hash    *string `json:"hash"`
	MaxInfo *User   `json:"max_info"`
}

// ContactRequestPayload — полезная нагрузка запроса на прикрепление контакта.
type ContactRequestPayload struct {
	Name      *string `json:"name"`
	ContactID *int64  `json:"contact_id,omitempty"`
	VcfInfo   *string `json:"vcf_info,omitempty"`
	VcfPhone  *string `json:"vcf_phone,omitempty"`
}

// MediaPayload — полезная нагрузка медиа-вложения (image/video/audio/file).
// `photo_id` заполняется только для image.
type MediaPayload struct {
	PhotoID int64  `json:"photo_id,omitempty"`
	Token   string `json:"token"`
	URL     string `json:"url"`
}

// MessageBody — содержимое сообщения. `text` обязателен и nullable.
type MessageBody struct {
	Mid         string          `json:"mid"`
	Seq         int64           `json:"seq"`
	Text        *string         `json:"text"`
	Attachments []Attachment    `json:"attachments,omitempty"`
	Markup      []MarkupElement `json:"markup,omitempty"`
}

// MessageStat — статистика поста (только для каналов; мок её не заполняет).
type MessageStat struct {
	Views int `json:"views"`
}

// LinkedMessage — пересланное или ответное сообщение.
type LinkedMessage struct {
	Type    string      `json:"type"`
	Sender  *User       `json:"sender,omitempty"`
	ChatID  *int64      `json:"chat_id,omitempty"`
	Message MessageBody `json:"message"`
}

// Message — сообщение в чате.
type Message struct {
	Sender    *User          `json:"sender,omitempty"`
	Recipient Recipient      `json:"recipient"`
	Timestamp int64          `json:"timestamp"`
	Link      *LinkedMessage `json:"link,omitempty"`
	Body      MessageBody    `json:"body"`
	Stat      *MessageStat   `json:"stat,omitempty"`
	URL       *string        `json:"url,omitempty"`
}

// SendMessageResult — ответ POST /messages.
type SendMessageResult struct {
	Message Message `json:"message"`
}

// MessageList — ответ GET /messages.
type MessageList struct {
	Messages []Message `json:"messages"`
}

// Callback — нажатие кнопки.
type Callback struct {
	Timestamp  int64  `json:"timestamp"`
	CallbackID string `json:"callback_id"`
	Payload    string `json:"payload,omitempty"`
	User       User   `json:"user"`
}

// Subscription — webhook-подписка в ответе GET /subscriptions.
// `update_types` обязателен и nullable.
type Subscription struct {
	URL         string   `json:"url"`
	Time        int64    `json:"time"`
	UpdateTypes []string `json:"update_types"`
}

// GetSubscriptionsResult — ответ GET /subscriptions.
type GetSubscriptionsResult struct {
	Subscriptions []Subscription `json:"subscriptions"`
}

// UploadEndpoint — ответ POST /uploads.
type UploadEndpoint struct {
	URL   string `json:"url"`
	Token string `json:"token,omitempty"`
}

// NewMessageLink — ссылка на сообщение в NewMessageBody.
type NewMessageLink struct {
	Type string `json:"type"`
	Mid  string `json:"mid"`
}

// NewMessageBody — тело POST/PUT /messages.
//
// Text, Attachments и Link объявлены в контракте обязательными и nullable.
// Различие «null» и «пустой массив» в Attachments значимо при правке:
// null — вложения не трогать, [] — удалить все. json.Unmarshal сохраняет это
// различие (null оставляет срез nil, [] делает его пустым, но не nil), поэтому
// AttachmentsSet отвечает на вопрос «поле пришло не-null».
type NewMessageBody struct {
	Text        *string             `json:"text"`
	Attachments []AttachmentRequest `json:"attachments"`
	Link        *NewMessageLink     `json:"link"`
	Notify      *bool               `json:"notify,omitempty"`
	Format      *string             `json:"format,omitempty"`

	attachmentsSet bool
}

// AttachmentsSet сообщает, пришёл ли `attachments` не-null значением.
func (b *NewMessageBody) AttachmentsSet() bool { return b.attachmentsSet }

// UnmarshalJSON разбирает тело, запоминая, было ли поле attachments null.
func (b *NewMessageBody) UnmarshalJSON(data []byte) error {
	type alias NewMessageBody
	var raw struct {
		alias
		Attachments json.RawMessage `json:"attachments"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*b = NewMessageBody(raw.alias)
	b.Attachments = nil
	b.attachmentsSet = false
	if len(raw.Attachments) > 0 && string(raw.Attachments) != "null" {
		if err := json.Unmarshal(raw.Attachments, &b.Attachments); err != nil {
			return err
		}
		if b.Attachments == nil {
			b.Attachments = []AttachmentRequest{}
		}
		b.attachmentsSet = true
	}
	return nil
}

// MarshalJSON пишет тело в контрактной форме: все три обязательных поля
// присутствуют, даже когда равны null.
func (b NewMessageBody) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Text        *string             `json:"text"`
		Attachments []AttachmentRequest `json:"attachments"`
		Link        *NewMessageLink     `json:"link"`
		Notify      *bool               `json:"notify,omitempty"`
		Format      *string             `json:"format,omitempty"`
	}{Text: b.Text, Attachments: b.Attachments, Link: b.Link, Notify: b.Notify, Format: b.Format})
}

// AttachmentRequest — запрос на прикрепление данных к сообщению.
// Полезная нагрузка остаётся сырой: её форма зависит от `type`, разбирается
// по месту использования (см. методы Payload*).
type AttachmentRequest struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
	// reply_keyboard хранит кнопки на верхнем уровне, а не в payload
	Buttons [][]Button `json:"buttons,omitempty"`
	// location
	Latitude  *float64 `json:"latitude,omitempty"`
	Longitude *float64 `json:"longitude,omitempty"`
	// reply_keyboard
	Direct       *bool  `json:"direct,omitempty"`
	DirectUserID *int64 `json:"direct_user_id,omitempty"`
}

// InlineKeyboardPayload — payload запроса inline_keyboard.
type InlineKeyboardPayload struct {
	Buttons [][]Button `json:"buttons"`
}

// PhotoToken — токен загруженного изображения.
type PhotoToken struct {
	Token string `json:"token"`
}

// PhotoRequestPayload — payload запроса на прикрепление изображения
// (поля взаимоисключающие).
type PhotoRequestPayload struct {
	URL   *string `json:"url,omitempty"`
	Token *string `json:"token,omitempty"`
}

// UploadedInfo — payload запроса на прикрепление video/audio/file.
type UploadedInfo struct {
	Token string `json:"token,omitempty"`
}

// SharePayload — payload запроса/вложения share.
type SharePayload struct {
	URL   *string `json:"url,omitempty"`
	Token *string `json:"token,omitempty"`
}

// StickerRequestPayload — payload запроса на прикрепление стикера.
type StickerRequestPayload struct {
	Code string `json:"code"`
}

// PayloadInlineKeyboard разбирает payload как клавиатуру.
func (a AttachmentRequest) PayloadInlineKeyboard() (InlineKeyboardPayload, error) {
	var p InlineKeyboardPayload
	err := json.Unmarshal(a.Payload, &p)
	return p, err
}

// PayloadPhoto разбирает payload как запрос изображения.
func (a AttachmentRequest) PayloadPhoto() (PhotoRequestPayload, error) {
	var p PhotoRequestPayload
	err := json.Unmarshal(a.Payload, &p)
	return p, err
}

// PayloadContact разбирает payload как запрос на прикрепление контакта.
func (a AttachmentRequest) PayloadContact() (ContactRequestPayload, error) {
	var p ContactRequestPayload
	err := json.Unmarshal(a.Payload, &p)
	return p, err
}

// PayloadUploaded разбирает payload как ссылку на загруженный медиафайл.
func (a AttachmentRequest) PayloadUploaded() (UploadedInfo, error) {
	var p UploadedInfo
	err := json.Unmarshal(a.Payload, &p)
	return p, err
}

// PayloadShare разбирает payload как запрос предпросмотра ссылки.
func (a AttachmentRequest) PayloadShare() (SharePayload, error) {
	var p SharePayload
	err := json.Unmarshal(a.Payload, &p)
	return p, err
}

// CallbackAnswer — тело POST /answers.
type CallbackAnswer struct {
	Message *NewMessageBody `json:"message,omitempty"`
}

// SubscriptionRequestBody — тело POST /subscriptions.
type SubscriptionRequestBody struct {
	URL         string   `json:"url"`
	UpdateTypes []string `json:"update_types,omitempty"`
	Secret      string   `json:"secret,omitempty"`
}

// UpdateBase — общие поля всех событий.
type UpdateBase struct {
	UpdateType string `json:"update_type"`
	Timestamp  int64  `json:"timestamp"`
}

// MessageCreatedUpdate — создано новое сообщение.
type MessageCreatedUpdate struct {
	UpdateBase
	Message    Message `json:"message"`
	UserLocale *string `json:"user_locale,omitempty"`
}

// MessageCallbackUpdate — пользователь нажал кнопку.
// `message` обязателен и nullable: исходное сообщение могло быть удалено.
type MessageCallbackUpdate struct {
	UpdateBase
	Callback   Callback `json:"callback"`
	Message    *Message `json:"message"`
	UserLocale *string  `json:"user_locale,omitempty"`
}

// MessageEditedUpdate — сообщение отредактировано.
type MessageEditedUpdate struct {
	UpdateBase
	Message Message `json:"message"`
}

// MessageRemovedUpdate — сообщение удалено.
type MessageRemovedUpdate struct {
	UpdateBase
	MessageID string `json:"message_id"`
	ChatID    int64  `json:"chat_id"`
	UserID    int64  `json:"user_id"`
}

// BotStartedUpdate — пользователь начал диалог с ботом.
type BotStartedUpdate struct {
	UpdateBase
	ChatID     int64   `json:"chat_id"`
	User       User    `json:"user"`
	Payload    *string `json:"payload,omitempty"`
	UserLocale *string `json:"user_locale,omitempty"`
}

// BotStoppedUpdate — пользователь остановил бота в его настройках.
// Payload-а у события нет: диплинк бывает только у запуска.
type BotStoppedUpdate struct {
	UpdateBase
	ChatID     int64   `json:"chat_id"`
	User       User    `json:"user"`
	UserLocale *string `json:"user_locale,omitempty"`
}

// Ptr возвращает указатель на значение — для необязательных и nullable полей.
func Ptr[T any](v T) *T { return &v }
