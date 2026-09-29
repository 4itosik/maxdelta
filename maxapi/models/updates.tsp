namespace MaxBotApi;

/**
 * Объект `Update` описывает возможные события в чате или канале. Может
 * возвращаться в следующих случаях:
 * - вы подписались на обновления через Webhook — при наступлении события
 *   МАКС пришлёт POST-запрос `/subscriptions`, который содержит объект `Update`;
 * - вы отправили GET-запрос `/updates` для получения обновлений через
 *   Long Polling — в ответ вернётся объект `Update`.
 */
@discriminator("update_type")
model Update {
  /** Тип события. 64 — в оригинале лимита нет; по аналогии с официальным лимитом коротких имён (BotCommand.name) */
  @maxLength(64)
  @pattern("^[a-z_]+$")
  update_type: string;

  /** Unix-время, когда произошло событие */
  @minValue(0)
  @maxValue(9007199254740991)
  timestamp: int64;
}

/** Вы получите это событие, как только сообщение будет создано */
model MessageCreatedUpdate extends Update {
  /** Тип события: `message_created` */
  update_type: "message_created";

  /** Новое созданное сообщение */
  message: Message;

  /** Текущий язык пользователя в формате IETF BCP 47. Доступно только в диалогах */
  @maxLength(255)
  @pattern("^[\\w\\W]{0,255}$")
  user_locale?: string | null;
}

/** Вы получите это событие, как только пользователь нажмёт кнопку */
model MessageCallbackUpdate extends Update {
  /** Тип события: `message_callback` */
  update_type: "message_callback";

  /** Нажатая пользователем кнопка */
  callback: Callback;

  /**
   * Изначальное сообщение, содержащее встроенную клавиатуру. Может быть
   * `null`, если оно было удалено к моменту, когда бот получил это событие
   */
  message: Message | null;

  /** Текущий язык пользователя в формате IETF BCP 47 */
  @maxLength(255)
  @pattern("^[\\w\\W]{0,255}$")
  user_locale?: string | null;
}

/** Вы получите это событие, как только пользователь отредактирует сообщение */
model MessageEditedUpdate extends Update {
  /** Тип события: `message_edited` */
  update_type: "message_edited";

  /** Отредактированное сообщение */
  message: Message;
}

/** Вы получите это событие, как только сообщение будет удалено */
model MessageRemovedUpdate extends Update {
  /** Тип события: `message_removed` */
  update_type: "message_removed";

  /** ID удалённого сообщения */
  message_id: MessageId;

  /** ID чата, где сообщение было удалено. Как получить ID — в разделе «Получение chat_id» */
  @minValue(0)
  @maxValue(9007199254740991)
  chat_id: int64;

  /** Пользователь, удаливший сообщение */
  @minValue(0)
  @maxValue(9007199254740991)
  user_id: int64;
}

/** Вы получите это событие, как только бот будет добавлен в чат или канал */
model BotAddedToChatUpdate extends Update {
  /** Тип события: `bot_added` */
  update_type: "bot_added";

  /** ID чата, куда был добавлен бот. Как получить ID — в разделе «Получение chat_id» */
  @minValue(0)
  @maxValue(9007199254740991)
  chat_id: int64;

  /** Пользователь, добавивший бота в чат */
  user: User;

  /** Указывает, что бот добавлен в канал, а не в чат */
  is_channel: boolean;
}

/** Вы получите это событие, как только бот будет удалён из чата */
model BotRemovedFromChatUpdate extends Update {
  /** Тип события: `bot_removed` */
  update_type: "bot_removed";

  /** ID чата, откуда был удалён бот. Как получить ID — в разделе «Получение chat_id» */
  @minValue(0)
  @maxValue(9007199254740991)
  chat_id: int64;

  /** Пользователь, удаливший бота из чата */
  user: User;

  /** Указывает, что бот удалён из канала, а не из чата */
  is_channel: boolean;
}

/**
 * Вы получите это событие, как только пользователь начнёт или возобновит
 * общение с ботом: нажмёт соответствующую кнопку в настройках бота в МАКС
 */
model BotStartedUpdate extends Update {
  /** Тип события: `bot_started` */
  update_type: "bot_started";

  /** ID диалога, где произошло событие. Как получить ID — в разделе «Получение chat_id» */
  @minValue(0)
  @maxValue(9007199254740991)
  chat_id: int64;

  /** Пользователь, который нажал кнопку `Start` */
  user: User;

  /** Дополнительные данные из диплинков, переданные при запуске бота. 512 — значение из официальной схемы MAX */
  @maxLength(512)
  @pattern("^[\\w\\W]{0,512}$")
  payload?: string | null;

  /** Текущий язык пользователя в формате IETF BCP 47 */
  @maxLength(255)
  @pattern("^[\\w\\W]{0,255}$")
  user_locale?: string;
}

/** Вы получите это событие, как только пользователь остановит бота в его настройках в МАКС */
model BotStoppedUpdate extends Update {
  /** Тип события: `bot_stopped` */
  update_type: "bot_stopped";

  /** ID диалога, где произошло событие. Как получить ID — в разделе «Получение chat_id» */
  @minValue(0)
  @maxValue(9007199254740991)
  chat_id: int64;

  /** Пользователь, который остановил бота */
  user: User;

  /** Текущий язык пользователя в формате IETF BCP 47 */
  @maxLength(255)
  @pattern("^[\\w\\W]{0,255}$")
  user_locale?: string;
}

/** Вы получите это событие, как только будет изменено название чата */
model ChatTitleChangedUpdate extends Update {
  /** Тип события: `chat_title_changed` */
  update_type: "chat_title_changed";

  /** ID чата, где произошло событие. Как получить ID — в разделе «Получение chat_id» */
  @minValue(0)
  @maxValue(9007199254740991)
  chat_id: int64;

  /** Пользователь, который изменил название */
  user: User;

  /** Новое название. 200 — официальный лимит названия чата (ChatButton.chat_title, ChatPatch.title) */
  @maxLength(200)
  @pattern("^[\\w\\W]{0,200}$")
  title: string;
}

/** Вы получите это событие, как только пользователь очистит историю сообщений */
model DialogClearedUpdate extends Update {
  /** Тип события: `dialog_cleared` */
  update_type: "dialog_cleared";

  /** ID чата, где произошло событие. Как получить ID — в разделе «Получение chat_id» */
  @minValue(0)
  @maxValue(9007199254740991)
  chat_id: int64;

  /** Пользователь, который очистил историю сообщений */
  user: User;

  /** Текущий язык пользователя в формате IETF BCP 47 */
  @maxLength(255)
  @pattern("^[\\w\\W]{0,255}$")
  user_locale?: string;
}

/**
 * Вы получите это событие, как только пользователь отключит уведомления о
 * новых сообщениях в диалоге с ботом
 */
model DialogMutedUpdate extends Update {
  /** Тип события: `dialog_muted` */
  update_type: "dialog_muted";

  /** ID чата, где произошло событие. Как получить ID — в разделе «Получение chat_id» */
  @minValue(0)
  @maxValue(9007199254740991)
  chat_id: int64;

  /** Пользователь, который отключил уведомления */
  user: User;

  /** Время в формате Unix, до наступления которого диалог был отключён */
  @minValue(0)
  @maxValue(9007199254740991)
  muted_until: int64;

  /** Текущий язык пользователя в формате IETF BCP 47 */
  @maxLength(255)
  @pattern("^[\\w\\W]{0,255}$")
  user_locale?: string;
}

/**
 * Вы получите это событие, как только пользователь включит уведомления о
 * новых сообщениях в диалоге с ботом
 */
model DialogUnmutedUpdate extends Update {
  /** Тип события: `dialog_unmuted` */
  update_type: "dialog_unmuted";

  /** ID чата, где произошло событие. Как получить ID — в разделе «Получение chat_id» */
  @minValue(0)
  @maxValue(9007199254740991)
  chat_id: int64;

  /** Пользователь, который включил уведомления */
  user: User;

  /** Текущий язык пользователя в формате IETF BCP 47 */
  @maxLength(255)
  @pattern("^[\\w\\W]{0,255}$")
  user_locale?: string;
}

/** Вы получите это событие, как только пользователь удалит чат */
model DialogRemovedUpdate extends Update {
  /** Тип события: `dialog_removed` */
  update_type: "dialog_removed";

  /** ID чата, где произошло событие. Как получить ID — в разделе «Получение chat_id» */
  @minValue(0)
  @maxValue(9007199254740991)
  chat_id: int64;

  /** Пользователь, который удалил чат */
  user: User;

  /** Текущий язык пользователя в формате IETF BCP 47 */
  @maxLength(255)
  @pattern("^[\\w\\W]{0,255}$")
  user_locale?: string;
}

/**
 * Вы получите это событие, как только пользователь будет добавлен в чат,
 * где бот является администратором
 */
model UserAddedToChatUpdate extends Update {
  /** Тип события: `user_added` */
  update_type: "user_added";

  /** ID чата, где произошло событие. Как получить ID — в разделе «Получение chat_id» */
  @minValue(0)
  @maxValue(9007199254740991)
  chat_id: int64;

  /** Пользователь, добавленный в чат */
  user: User;

  /**
   * Пользователь, который добавил нового пользователя в чат. Может быть
   * `null`, если пользователь присоединился к чату по ссылке
   */
  @minValue(0)
  @maxValue(9007199254740991)
  inviter_id?: int64 | null;

  /** Указывает, что пользователь добавлен в канал, а не в чат */
  is_channel: boolean;
}

/**
 * Вы получите это событие, как только пользователь будет удалён из чата,
 * где бот является администратором
 */
model UserRemovedFromChatUpdate extends Update {
  /** Тип события: `user_removed` */
  update_type: "user_removed";

  /** ID чата, где произошло событие. Как получить ID — в разделе «Получение chat_id» */
  @minValue(0)
  @maxValue(9007199254740991)
  chat_id: int64;

  /** Пользователь, удалённый из чата */
  user: User;

  /**
   * Администратор, который удалил пользователя из чата. Может быть `null`,
   * если пользователь покинул чат сам
   */
  @minValue(0)
  @maxValue(9007199254740991)
  admin_id?: int64;

  /** Указывает, что пользователь удалён из канала, а не из чата */
  is_channel: boolean;
}

// Событие message_chat_created (MessageChatCreatedUpdate) убрано намеренно.
// Его нет ни в официальной спецификации github.com/max-messenger/api-schema,
// ни в каталоге событий на dev.max.ru; в reference/ оно есть, но и там не
// попало в Update.discriminator.mapping. Оно же — единственное, что тянуло в
// контракт схему Chat, а с ней словарь Chat.participants
// (additionalProperties: {$ref: SafeInt64}), который КБ-валидатор в Jenkins
// отвергает: у object-схемы должно быть additionalProperties: false.
// Приходит оно только при создании группового чата кнопкой `chat` — сценарий
// вне объёма; если MAX его всё же пришлёт, периметр его отвергнет.

/**
 * Список обновлений о событиях в чатах и каналах, в которые добавлен бот.
 * Обратите внимание, чтобы получать события из групповых чатов и каналов,
 * бот должен быть администратором
 */
model UpdateList {
  /** Список обновлений о событиях в чатах и каналах, в которые добавлен бот. 1000 — зеркало максимума параметра limit у GET /updates */
  @maxItems(1000)
  updates: Update[];

  /** Указатель на следующую страницу данных */
  @minValue(0)
  @maxValue(9007199254740991)
  marker: int64 | null;
}

/** Схема для описания подписки на WebHook */
model Subscription {
  /** URL вебхука. 2048 — официальный лимит URL в оригинальной схеме (LinkButton.url, LinkMarkup.url) */
  @maxLength(2048)
  @pattern("^[\\w\\W]{0,2048}$")
  url: string;

  /** Unix-время, когда была создана подписка */
  @minValue(0)
  @maxValue(9007199254740991)
  time: int64;

  /**
   * Типы событий, на которые подписан бот.
   *
   * Элементы типизированы scalar-ом `UpdateType` (КБ: pattern + maxLength
   * для строк); его `minLength: 1` заодно совпадает с ограничением
   * элементов в оригинале.
   */
  @maxItems(50)
  update_types: UpdateType[] | null;
}

/** Запрос на настройку подписки WebHook */
model SubscriptionRequestBody {
  /** URL HTTPS-endpoint вашего бота. Должен начинаться с `https://`. 2048 — официальный лимит URL в оригинальной схеме (LinkButton.url, LinkMarkup.url) */
  @maxLength(2048)
  @pattern("^https://.+$")
  url: string;

  /** Список типов событий, которые хочет получать ваш бот */
  @maxItems(50)
  update_types?: UpdateType[];

  /**
   * Секрет, который должен быть отправлен в заголовке `X-Max-Bot-Api-Secret`
   * в каждом запросе Webhook. Разрешены только символы `A-Z`, `a-z`, `0-9`,
   * и дефис. Заголовок рекомендован, чтобы запрос поступал из установленного
   * веб-узла. 5..256 — значение из официальной схемы MAX
   */
  @minLength(5)
  @maxLength(256)
  @pattern("^[a-zA-Z0-9_-]{5,256}$")
  secret?: string;
}

/** Список всех WebHook подписок */
model GetSubscriptionsResult {
  /** Список текущих подписок */
  @maxItems(100)
  subscriptions: Subscription[];
}
