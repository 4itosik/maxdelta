import "@typespec/http";
import "@typespec/openapi";
import "@typespec/openapi3";
import "./models/updates.tsp";

using Http;
using OpenAPI;

/**
 * Контракт webhook-эндпоинта, который реализует РАЗРАБОТЧИК БОТА (а не
 * платформа MAX). После регистрации URL через `POST /subscriptions`
 * платформа доставляет события бота HTTPS POST-запросами на этот эндпоинт.
 *
 * Требования платформы: HTTPS, порт 443, сертификат от доверенного центра
 * (самоподписные и HTTP не поддерживаются с 25 мая 2026). Отвечайте 200 OK
 * как можно быстрее; обработку события выполняйте после ответа.
 */
@service(#{ title: "Max Bot Webhook" })
// TODO: contact — плейсхолдер (требование SCHEMA-валидатора: info.contact
// обязателен); заменить на реальные контакты команды перед публикацией.
@info(#{
  contact: #{
    name: "Max Bot Webhook (placeholder)",
    url: "https://dev.max.ru/docs-api",
    email: "bot-api-placeholder@example.com",
  },
})
@server("https://bot.example.ru", "Ваш webhook-endpoint (URL из POST /subscriptions)")
namespace MaxBotWebhook;

@tag("webhook-server")
interface WebhookReceiver {
  /**
   * Приём события от платформы MAX. Тело описано двумя эквивалентными
   * способами (anyOf — любое событие соответствует обоим):
   * `UpdateUnified` — единая «плоская» схема (общие поля обязательны,
   * вариативные опциональны, у каждого указано, при каких `update_type`
   * приходит); `WebhookUpdate` — строгий oneOf из 16 конкретных типов
   * для типизированной кодогенерации.
   *
   * Если при подписке был задан `secret`, платформа передаёт его в заголовке
   * `X-Max-Bot-Api-Secret` — проверяйте его, чтобы убедиться, что запрос
   * пришёл от MAX, а не от постороннего.
   */
  @summary("Приём события от платформы MAX")
  @route("/max/v1.0/webhooks/{integrationId}")
  @post
  receiveUpdate(
    /**
     * Идентификатор интеграции — последний сегмент URL, который вы
     * регистрируете через `POST /subscriptions`. UUID (RFC 4122): 36
     * символов, hex-группы 8-4-4-4-12 через дефисы. Тип строковый, а не
     * отдельный uuid-скаляр: в OpenAPI 3.0 `format: uuid` — только
     * аннотация, реально ограничивают pattern и длина.
     */
    @format("uuid")
    @minLength(36)
    @maxLength(36)
    @pattern("^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$")
    @path integrationId: string,

    /** Секрет, заданный при создании подписки (см. SubscriptionRequestBody.secret). 5..256 — значение из официальной схемы MAX */
    @minLength(5)
    @maxLength(256)
    @pattern("^[a-zA-Z0-9_-]{5,256}$")
    @header("X-Max-Bot-Api-Secret") secret?: string,

    /** Событие; какие поля заполнены — определяется значением update_type */
    @body update: UpdateUnified | WebhookUpdate,
  ): AcceptedResponse;
}

/**
 * Единая («плоская») схема события: объединение полей всех 15 типов
 * `Update` в один объект — в стиле Telegram Bot API. Обязательны только
 * `update_type` и `timestamp`; остальные поля приходят в зависимости от
 * типа события. Удобна, когда на сервере хочется один класс/структуру
 * на все события без union-машинерии.
 */
model UpdateUnified {
  /**
   * Тип события: message_created | message_callback | message_edited |
   * message_removed | bot_added | bot_removed | bot_started | bot_stopped |
   * chat_title_changed | dialog_cleared | dialog_muted | dialog_unmuted |
   * dialog_removed | user_added | user_removed
   */
  @maxLength(64)
  @pattern("^[a-z_]+$")
  update_type: string;

  /** Unix-время, когда произошло событие */
  @minValue(0)
  @maxValue(9007199254740991)
  timestamp: int64;

  /** ID чата. [bot_added, bot_started, bot_stopped, bot_removed, chat_title_changed, dialog_cleared, dialog_muted, dialog_unmuted, dialog_removed, message_removed, user_added, user_removed] */
  @minValue(0)
  @maxValue(9007199254740991)
  chat_id?: int64;

  /** Пользователь-инициатор события. [bot_added, bot_started, bot_stopped, bot_removed, chat_title_changed, dialog_cleared, dialog_muted, dialog_unmuted, dialog_removed, user_added, user_removed] */
  user?: MaxBotApi.User;

  /** Текущий язык пользователя (IETF BCP 47), доступен только в диалогах. [bot_started, bot_stopped, dialog_cleared, dialog_muted, dialog_unmuted, dialog_removed, message_created, message_callback] */
  @maxLength(255)
  @pattern("^[\\w\\W]{0,255}$")
  user_locale?: string | null;

  /** Признак «событие в канале, а не в чате». [bot_added, bot_removed, user_added, user_removed] */
  is_channel?: boolean;

  /** Сообщение: новое, отредактированное или с нажатой кнопкой (может быть null). [message_created, message_edited, message_callback] */
  message?: MaxBotApi.Message | null;

  /** Нажатие callback-кнопки. [message_callback] */
  callback?: MaxBotApi.Callback;

  /** Новое название чата. 200 — официальный лимит названия чата (ChatButton.chat_title, ChatPatch.title). [chat_title_changed] */
  @maxLength(200)
  @pattern("^[\\w\\W]{0,200}$")
  title?: string;

  /** Данные из диплинка, переданные при запуске бота. 512 — значение из официальной схемы MAX (BotStartedUpdate.payload). [bot_started] */
  @maxLength(512)
  @pattern("^[\\w\\W]{0,512}$")
  payload?: string | null;

  /** До какого времени отключены уведомления (Unix-время). [dialog_muted] */
  @minValue(0)
  @maxValue(9007199254740991)
  muted_until?: int64;

  /** Кто пригласил пользователя; null, если вошёл по ссылке. [user_added] */
  @minValue(0)
  @maxValue(9007199254740991)
  inviter_id?: int64 | null;

  /** Администратор, удаливший пользователя; отсутствует, если пользователь вышел сам. [user_removed] */
  @minValue(0)
  @maxValue(9007199254740991)
  admin_id?: int64;

  /** ID удалённого сообщения. [message_removed] */
  @minLength(1)
  @maxLength(255)
  @pattern("^[a-zA-Z0-9._\\-]+$")
  message_id?: string;

  /** Автор удалённого сообщения. [message_removed] */
  @minValue(0)
  @maxValue(9007199254740991)
  user_id?: int64;
}

/**
 * Явный oneOf всех 15 типов событий — чтобы Swagger UI и генераторы
 * показывали полный список вариантов, а не только базовые поля Update
 * (allOf-наследование дискриминированного union'а UI не разворачивают).
 */
@oneOf
@discriminated(#{ envelope: "none", discriminatorPropertyName: "update_type" })
union WebhookUpdate {
  message_created: MaxBotApi.MessageCreatedUpdate,
  message_callback: MaxBotApi.MessageCallbackUpdate,
  message_edited: MaxBotApi.MessageEditedUpdate,
  message_removed: MaxBotApi.MessageRemovedUpdate,
  bot_added: MaxBotApi.BotAddedToChatUpdate,
  bot_removed: MaxBotApi.BotRemovedFromChatUpdate,
  bot_started: MaxBotApi.BotStartedUpdate,
  bot_stopped: MaxBotApi.BotStoppedUpdate,
  chat_title_changed: MaxBotApi.ChatTitleChangedUpdate,
  dialog_cleared: MaxBotApi.DialogClearedUpdate,
  dialog_muted: MaxBotApi.DialogMutedUpdate,
  dialog_unmuted: MaxBotApi.DialogUnmutedUpdate,
  dialog_removed: MaxBotApi.DialogRemovedUpdate,
  user_added: MaxBotApi.UserAddedToChatUpdate,
  user_removed: MaxBotApi.UserRemovedFromChatUpdate,
}

/** Подтверждение приёма: платформа ожидает 200 OK без тела */
model AcceptedResponse {
  @statusCode statusCode: 200;
}
