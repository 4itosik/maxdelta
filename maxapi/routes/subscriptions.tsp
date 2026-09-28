import "@typespec/http";
import "@typespec/openapi";

using Http;
using OpenAPI;

namespace MaxBotApi;

@tag("subscriptions")
interface Subscriptions {
  /** Если ваш бот получает данные через Webhook, этот метод возвращает список всех подписок */
  @summary("Получение всех подписок через Webhook")
  @operationId("getSubscriptions")
  @route("/subscriptions")
  @get
  getSubscriptions(): GetSubscriptionsResult | Unauthorized | InternalError;

  /** Настраивает доставку событий бота через Webhook */
  @summary("Подписка на обновления о новых событиях через Webhook")
  @operationId("subscribe")
  @route("/subscriptions")
  @post
  subscribe(
    @body body: SubscriptionRequestBody,
  ): SimpleQueryResult | Unauthorized | InternalError;

  /** Отписывает бота от получения обновлений о новых событиях через Webhook */
  @summary("Отписка от обновлений о новых событиях через Webhook")
  @operationId("unsubscribe")
  @route("/subscriptions")
  @delete
  unsubscribe(
    /**
     * URL, который нужно удалить из подписок на WebHook. 2048 — официальный
     * лимит URL в оригинальной схеме (LinkButton.url, LinkMarkup.url).
     *
     * Шаблон намеренно «любая строка», а не `^https://.+$`, как у
     * SubscriptionRequestBody.url: в query значение приходит
     * percent-encoded (`https%3A%2F%2F…`), и валидатор периметра (Atlassian)
     * сверяет его с шаблоном до декодирования — строгий шаблон отвергал
     * каждую отписку. Требование `https://` остаётся на подписке, где URL
     * лежит в JSON-теле и не кодируется.
     */
    @minLength(1)
    @maxLength(2048)
    @pattern("^[\\w\\W]{1,2048}$")
    @query url: string,
  ): SimpleQueryResult | Unauthorized | InternalError;

  /** Получение обновлений о событиях через Long Polling */
  @summary("Получение обновлений о событиях через Long Polling")
  @operationId("getUpdates")
  @route("/updates")
  @get
  getUpdates(
    /** Максимальное количество обновлений о событиях, которое может вернуться в ответе на запрос */
    @minValue(1)
    @maxValue(1000)
    @query limit?: integer = 100,

    /** Тайм-аут в секундах для долгого опроса */
    @minValue(0)
    @maxValue(90)
    @query timeout?: integer = 30,

    /** Чтобы получить последнее обновление событий, передайте `null` или оставьте поле пустым. Чтобы получить все обновления по событиям с момента предыдущего запроса, передайте значение `marker`, которое получили в ответ на предыдущий запрос */
    @minValue(0)
    @maxValue(9007199254740991)
    @query marker?: int64 | null,

    /** Список типов событий, которые вы хотите получать, например: `message_created`, `message_callback` */
    @maxItems(50)
    @query types?: UpdateType[] | null,
  ): UpdateList | Unauthorized | NotAllowed | InternalError;
}
