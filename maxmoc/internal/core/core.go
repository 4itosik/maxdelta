// Package core — доменная логика мока: что происходит с состоянием, когда
// бот вызывает Bot API и когда тестировщик действует за клиента.
//
// Слой не знает про HTTP: фасад Bot API и control-API вызывают одни и те же
// операции. Побочные эффекты каждой операции — запись в хранилище, публикация
// в шину для UI и постановка события в очередь доставки — собраны здесь, чтобы
// не расползались по обработчикам.
package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"time"

	"maxmock/internal/config"
	"maxmock/internal/events"
	"maxmock/internal/store"
	"maxmock/internal/webhook"
	"maxmock/internal/wire"
)

// Доменные ошибки. Фасад переводит их в коды `mock.*`, выбирая статус по
// тому, объявлен ли он в контракте для конкретной операции.
var (
	ErrChatNotFound       = errors.New("диалог не найден")
	ErrMessageNotFound    = errors.New("сообщение не найдено")
	ErrCallbackNotFound   = errors.New("нажатие кнопки не найдено")
	ErrAttachmentNotFound = errors.New("вложение не найдено")
	ErrForbidden          = errors.New("операция запрещена")
	ErrBadRequest         = errors.New("некорректный запрос")
)

// Core — доменный слой.
type Core struct {
	store *store.Store
	disp  *webhook.Dispatcher
	bus   *events.Bus
	cfg   *config.Config
}

// New собирает доменный слой.
func New(st *store.Store, disp *webhook.Dispatcher, bus *events.Bus, cfg *config.Config) *Core {
	return &Core{store: st, disp: disp, bus: bus, cfg: cfg}
}

// Store даёт доступ к хранилищу (нужен control-API для чтения списков).
func (c *Core) Store() *store.Store { return c.store }

// Config возвращает конфигурацию.
func (c *Core) Config() *config.Config { return c.cfg }

func nowMS() int64 { return time.Now().UnixMilli() }

// FileURL — публичная ссылка на залитый в мок файл.
func (c *Core) FileURL(token string) string {
	return c.cfg.PublicBaseURL + "/mock/files/" + token
}

// UploadURL — адрес, куда КЦ заливает файл после POST /uploads.
func (c *Core) UploadURL(token string) string {
	return c.cfg.PublicBaseURL + "/mock/upload/" + token
}

// textOrEmpty приводит отсутствующий текст сообщения к пустой строке.
//
// В запросе `text: null` означает «текста нет» (а при правке — «не менять»),
// но в готовом сообщении живой Max отдаёт пустую строку: сообщение из одних
// вложений приезжает с `"text":""`. Разница видна каждому клиенту, который
// трогает текст без проверки на null, и мок обязан ронять его там же, где
// уронит прод, — то есть нигде.
func textOrEmpty(text *string) *string {
	if text == nil {
		return wire.Ptr("")
	}
	return text
}

// userLastActivityMS — время последней активности пользователя с той
// точностью, с какой его отдаёт живой Max: до секунды (в образцах с прода
// 1786079712000 и 1786081137000 при timestamp сообщений …712219 и …136058).
//
// Ботам эта грубость не передаётся: у них прод отдаёт время с миллисекундами
// (1786081137409 в ответе POST /messages), и botUser берёт часы как есть.
// Похоже, у пользователя это отдельно хранимое «был в сети», у бота — просто
// текущее время.
func userLastActivityMS() int64 { return nowMS() / 1000 * 1000 }

// botUser описывает бота в терминах схемы User. Фамилии у бота нет — контракт
// прямо говорит, что для ботов поле не возвращается.
func botUser(b *store.Bot) wire.User {
	return wire.User{
		UserID:           b.UserID,
		FirstName:        b.Name,
		Username:         wire.Ptr(b.Username),
		IsBot:            true,
		LastActivityTime: nowMS(),
		Name:             wire.Ptr(b.Name),
	}
}

// clientUser описывает виртуального клиента в терминах схемы User.
//
// Фамилия проставляется всегда, даже пустой строкой: так её отдаёт живой Max.
// Публичное имя, наоборот, при отсутствии не отдаётся вовсе (см. wire.User).
func clientUser(cl *store.Client) wire.User {
	u := wire.User{
		UserID:           cl.UserID,
		FirstName:        cl.FirstName,
		LastName:         wire.Ptr(cl.LastName),
		IsBot:            false,
		LastActivityTime: userLastActivityMS(),
		Name:             wire.Ptr(clientFullName(cl)),
	}
	if cl.Username != "" {
		u.Username = wire.Ptr(cl.Username)
	}
	return u
}

// BotInfo — ответ GET /me.
func (c *Core) BotInfo(b *store.Bot) wire.BotInfo {
	info := wire.BotInfo{User: botUser(b), Commands: botCommands(b)}
	if b.Description != "" {
		info.Description = wire.Ptr(b.Description)
	}
	return info
}

// botCommands разбирает список команд бота. Битый JSON в этом поле — сбой
// хранилища, а не запроса, поэтому команда просто не показывается: ронять
// GET /me из-за неё нельзя.
func botCommands(b *store.Bot) []wire.BotCommand {
	if len(b.Commands) == 0 {
		return nil
	}
	var out []wire.BotCommand
	if err := json.Unmarshal(b.Commands, &out); err != nil {
		return nil
	}
	return out
}

// EditMyCommands — PATCH /me/commands.
//
// Пустой список удаляет все команды (так описана операция в контракте),
// отсутствие поля оставляет их без изменений.
func (c *Core) EditMyCommands(b *store.Bot, patch wire.BotCommandsPatch) (*wire.BotCommandsInfo, error) {
	if !patch.CommandsSet() {
		return &wire.BotCommandsInfo{Commands: emptyIfNil(botCommands(b))}, nil
	}
	raw, err := json.Marshal(emptyIfNil(patch.Commands))
	if err != nil {
		return nil, err
	}
	if err := c.store.UpdateBotCommands(b.ID, raw); err != nil {
		return nil, err
	}
	b.Commands = raw
	// Веб-чат рисует меню команд из этого поля: без уведомления полоса
	// обновлялась бы только перезагрузкой страницы.
	c.bus.Publish(events.Event{
		Kind: events.KindBot, BotID: b.ID,
		Payload: map[string]any{"commands": emptyIfNil(patch.Commands)},
	})
	return &wire.BotCommandsInfo{Commands: emptyIfNil(patch.Commands)}, nil
}

func emptyIfNil(commands []wire.BotCommand) []wire.BotCommand {
	if commands == nil {
		return []wire.BotCommand{}
	}
	return commands
}

// recipient описывает получателя сообщения в диалоге.
//
// В диалоге две стороны, и получатель — та, которой сообщение адресовано:
// когда пишет бот, это клиент; когда пишет клиент, это бот. Поле user_id в
// контракте так и описано — «ID пользователя, если сообщение было отправлено
// пользователю» (бот в схемах Max тоже User).
func recipient(chatID, recipientUserID int64) wire.Recipient {
	return wire.Recipient{
		ChatID:   wire.Ptr(chatID),
		ChatType: wire.ChatTypeDialog,
		UserID:   wire.Ptr(recipientUserID),
	}
}

// dialogParties возвращает бота и клиента диалога.
func (c *Core) dialogParties(d *store.Dialog) (*store.Bot, *store.Client, error) {
	bot, err := c.store.BotByID(d.BotID)
	if err != nil {
		return nil, nil, err
	}
	cl, err := c.store.ClientByUserID(d.UserID)
	if err != nil {
		return nil, nil, err
	}
	return bot, cl, nil
}

// buildMessage восстанавливает wire.Message из записи хранилища.
func (c *Core) buildMessage(m *store.Message) (wire.Message, error) {
	d, err := c.store.DialogByChatID(m.ChatID)
	if err != nil {
		return wire.Message{}, err
	}
	bot, cl, err := c.dialogParties(d)
	if err != nil {
		return wire.Message{}, err
	}

	var body wire.MessageBody
	if err := json.Unmarshal(m.Body, &body); err != nil {
		return wire.Message{}, fmt.Errorf("разбор тела сообщения %s: %w", m.Mid, err)
	}

	sender, recipientUserID := botUser(bot), cl.UserID
	if m.Sender == store.SenderClient {
		sender, recipientUserID = clientUser(cl), bot.UserID
	}
	return wire.Message{
		Sender:    &sender,
		Recipient: recipient(m.ChatID, recipientUserID),
		Timestamp: m.CreatedAt,
		Body:      body,
	}, nil
}

// photoID выводит числовой идентификатор изображения из его токена:
// контракт требует photo_id, а в моке единственный устойчивый источник
// идентичности вложения — токен.
//
// Хэш урезается до 52 бит по той же причине, по которой держатся в этом
// диапазоне chat_id и user_id (см. ids.maxSafeID): JSON-числа в JavaScript —
// это float64, и целые больше 2^53−1 там теряют точность. Контракт объявляет
// этот предел явно (maximum: 9007199254740991), а прежний сдвиг на 1 бит
// давал 63-битные значения — вне контракта. Расхождение не всплывало, пока
// вложения валидировались против базовой схемы Attachment, у которой из всех
// свойств проверялся один `type`; после перевода дискриминированных union'ов
// в форму oneOf проверяется конкретный вариант — и оно всплыло.
func photoID(token string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(token))
	return int64(h.Sum64() >> 12)
}

// withButtonDefaults достраивает у кнопок клавиатуры умолчания, которые живой
// Max материализует в теле сообщения.
//
// Такое умолчание одно: `quick` у request_geo_location. Контракт объявляет
// его через `default: false`, и прод возвращает поле даже там, где в запросе
// его не было («Отправить геопозицию» в образце уехала без `quick`, а
// приехала с `"quick":false`). Остальные кнопки прод возвращает как присланы
// — у них поля `quick` в схеме нет вовсе, и дописывать его нельзя.
//
// Замер сделан на inline_keyboard; reply_keyboard использует ту же схему
// кнопки, поэтому умолчание достраивается и там.
func withButtonDefaults(rows [][]wire.Button) [][]wire.Button {
	out := make([][]wire.Button, 0, len(rows))
	for _, row := range rows {
		cooked := make([]wire.Button, 0, len(row))
		for _, b := range row {
			if b.Type == wire.ButtonRequestGeoLocation && b.Quick == nil {
				b.Quick = wire.Ptr(false)
			}
			cooked = append(cooked, b)
		}
		out = append(out, cooked)
	}
	return out
}

// attachmentFromRequest переводит запрос на прикрепление в вложение сообщения.
//
// Неизвестные типы пропускаются как есть: контракт описывает вложения
// дискриминируемым объединением, и мок не обязан понимать каждый вариант,
// чтобы вернуть его в теле сообщения.
func (c *Core) attachmentFromRequest(req wire.AttachmentRequest) (wire.Attachment, error) {
	att := wire.Attachment{Type: req.Type}

	switch req.Type {
	case wire.AttachmentInlineKeyboard:
		p, err := req.PayloadInlineKeyboard()
		if err != nil {
			return att, fmt.Errorf("%w: клавиатура: %v", ErrBadRequest, err)
		}
		payload, err := json.Marshal(wire.Keyboard{Buttons: withButtonDefaults(p.Buttons)})
		if err != nil {
			return att, err
		}
		att.Payload = payload

	case wire.AttachmentReplyKeyboard:
		att.Buttons = withButtonDefaults(req.Buttons)

	case wire.AttachmentImage:
		p, err := req.PayloadPhoto()
		if err != nil {
			return att, fmt.Errorf("%w: изображение: %v", ErrBadRequest, err)
		}
		token, url := c.resolvePhoto(p)
		if token == "" && url == "" {
			return att, fmt.Errorf("%w: в изображении не задан ни token, ни url, ни photos", ErrBadRequest)
		}
		payload, err := json.Marshal(wire.MediaPayload{PhotoID: photoID(token + url), Token: token, URL: url})
		if err != nil {
			return att, err
		}
		att.Payload = payload

	case wire.AttachmentVideo, wire.AttachmentAudio, wire.AttachmentFile:
		p, err := req.PayloadUploaded()
		if err != nil {
			return att, fmt.Errorf("%w: медиа-вложение: %v", ErrBadRequest, err)
		}
		if p.Token == "" {
			return att, fmt.Errorf("%w: в медиа-вложении не задан token", ErrBadRequest)
		}
		stored, err := c.store.AttachmentByToken(p.Token)
		if errors.Is(err, store.ErrNotFound) {
			return att, ErrAttachmentNotFound
		}
		if err != nil {
			return att, err
		}
		payload, err := json.Marshal(wire.MediaPayload{Token: stored.Token, URL: c.FileURL(stored.Token)})
		if err != nil {
			return att, err
		}
		att.Payload = payload
		if req.Type == wire.AttachmentFile {
			att.Filename = stored.Filename
			att.Size = wire.Ptr(stored.Size)
		}

	case wire.AttachmentLocation:
		att.Latitude, att.Longitude = req.Latitude, req.Longitude

	case wire.AttachmentContact:
		// Формы запроса и сообщения различаются: в запросе name/contact_id/
		// vcf_phone, в сообщении vcf_info/hash/max_info. Без перевода стенд
		// получил бы вложение, которого живой Max не пришлёт, а валидация
		// этого не поймает — лишние поля схема не запрещает.
		p, err := req.PayloadContact()
		if err != nil {
			return att, fmt.Errorf("%w: контакт: %v", ErrBadRequest, err)
		}
		payload, err := json.Marshal(c.contactPayloadFromRequest(p))
		if err != nil {
			return att, err
		}
		att.Payload = payload

	default:
		att.Payload = req.Payload
		att.Buttons = req.Buttons
		att.Latitude, att.Longitude = req.Latitude, req.Longitude
	}
	return att, nil
}

// resolvePhoto выбирает токен и URL изображения из взаимоисключающих полей
// запроса: прямая ссылка или токен ранее загруженного файла. Словаря photos
// в запросе контракт больше не допускает — токен из ответа загрузки бот
// кладёт в token.
func (c *Core) resolvePhoto(p wire.PhotoRequestPayload) (token, url string) {
	if p.Token != nil && *p.Token != "" {
		token = *p.Token
	}
	if token != "" {
		if stored, err := c.store.AttachmentByToken(token); err == nil {
			return stored.Token, c.FileURL(stored.Token)
		}
		return token, c.FileURL(token)
	}
	if p.URL != nil {
		// Внешняя ссылка: токен придумываем сами, ссылку оставляем как есть —
		// контракт требует оба поля.
		return "ext." + fmt.Sprintf("%016x", uint64(photoID(*p.URL))), *p.URL
	}
	return "", ""
}

// buildAttachments переводит все вложения запроса в вложения сообщения.
//
// Вызывается ДО открытия транзакции сохранения: конвертация читает хранилище
// (метаданные загруженных файлов), а транзакция занимает единственное
// соединение пула — вложенный запрос встал бы намертво.
func (c *Core) buildAttachments(req wire.NewMessageBody) ([]wire.Attachment, error) {
	if len(req.Attachments) == 0 {
		return nil, nil
	}
	// Требование контракта: карточка контакта должна быть единственным
	// вложением сообщения.
	if len(req.Attachments) > 1 {
		for _, ar := range req.Attachments {
			if ar.Type == wire.AttachmentContact {
				return nil, fmt.Errorf(
					"%w: вложение contact должно быть единственным в сообщении", ErrBadRequest)
			}
		}
	}
	out := make([]wire.Attachment, 0, len(req.Attachments))
	for _, ar := range req.Attachments {
		att, err := c.attachmentFromRequest(ar)
		if err != nil {
			return nil, err
		}
		out = append(out, att)
	}
	return out, nil
}

// publish отправляет событие в шину UI и ставит его в очередь доставки.
func (c *Core) publish(kind string, botID, chatID int64, uiPayload any, updateType string, update any) {
	c.bus.Publish(events.Event{Kind: kind, BotID: botID, ChatID: chatID, Payload: uiPayload})
	if updateType != "" {
		_ = c.disp.Deliver(botID, chatID, updateType, update)
	}
}
