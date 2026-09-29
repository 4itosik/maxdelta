namespace MaxBotApi;

/** Запрос на прикрепление данных к сообщению */
@discriminator("type")
model AttachmentRequest {
  /** Тип вложения. 64 — в оригинале лимита нет; по аналогии с официальным лимитом коротких имён (BotCommand.name) */
  @maxLength(64)
  @pattern("^[a-z_]+$")
  type: string;
}

/** Токен, полученный после загрузки изображения */
model PhotoToken {
  /** Закодированная информация загруженного изображения. 1024 — в оригинале лимита нет; по аналогии с официальным лимитом кнопочного payload (CallbackButton.payload) */
  @maxLength(1024)
  @pattern("^[\\w\\W]{0,1024}$")
  token: string;
}

/** Это информация, которую вы получите, как только изображение будет загружено */
model PhotoTokens {
  /** Токены загруженных изображений */
  photos: Record<PhotoToken>;
}

/**
 * Запрос на прикрепление изображения (все поля являются взаимоисключающими).
 *
 * ОТЛИЧИЕ ОТ ОРИГИНАЛА (осознанное, см. DEVIATIONS в scripts/compare.py):
 * поля `photos` (словарь токенов из ответа на загрузку) здесь нет. Словарь —
 * это `additionalProperties: {$ref: PhotoToken}`, а КБ-валидатор в Jenkins
 * требует у object-схемы `additionalProperties: false`. Документация MAX
 * отправляет фото через `token` (или `url`): токен берётся из словаря
 * `photos` в ответе на загрузку и кладётся в `token`.
 */
model PhotoAttachmentRequestPayload {
  /** Любой внешний URL изображения, которое вы хотите прикрепить. 2048 — официальный лимит URL в оригинальной схеме (LinkButton.url, LinkMarkup.url) */
  @minLength(1)
  @maxLength(2048)
  @pattern("^[\\w\\W]{1,2048}$")
  url?: string | null;

  /** Токен существующего вложения. 1024 — в оригинале лимита нет; по аналогии с официальным лимитом кнопочного payload (CallbackButton.payload) */
  @maxLength(1024)
  @pattern("^[\\w\\W]{0,1024}$")
  token?: string | null;
}

/** Это информация, которую вы получите, как только аудио/видео будет загружено */
model UploadedInfo {
  /** Токен — уникальный ID загруженного медиафайла. 1024 — в оригинале лимита нет; по аналогии с официальным лимитом кнопочного payload (CallbackButton.payload) */
  @maxLength(1024)
  @pattern("^[\\w\\W]{0,1024}$")
  token?: string;
}

/** Полезная нагрузка запроса на прикрепление карточки контакта */
model ContactAttachmentRequestPayload {
  /** Имя контакта */
  @maxLength(255)
  @pattern("^[\\w\\W]{0,255}$")
  name: string | null;

  /** ID контакта, если он зарегистрирован в MAX */
  @minValue(0)
  @maxValue(9007199254740991)
  contact_id?: int64 | null;

  /** Полная информация о контакте в формате VCF. 4096 — в оригинале лимита нет; запас под VCF-карточку */
  @maxLength(4096)
  @pattern("^[\\w\\W]{0,4096}$")
  vcf_info?: string | null;

  /** Телефон контакта в формате VCF */
  @maxLength(255)
  @pattern("^[\\w\\W]{0,255}$")
  vcf_phone?: string | null;
}

/** Полезная нагрузка запроса на прикрепление стикера */
model StickerAttachmentRequestPayload {
  /** Код стикера */
  @maxLength(255)
  @pattern("^[\\w\\W]{0,255}$")
  code: string;
}

/** Полезная нагрузка запроса на прикрепление клавиатуры */
model InlineKeyboardAttachmentRequestPayload {
  /** Двумерный массив кнопок. Подробнее о типах кнопок и клавиатуре в ботах — в разделе «Клавиатура для чат-бота» */
  @minItems(1)
  @maxItems(100)
  buttons: ButtonRow[];
}

/**
 * Тип загружаемого медиафайла. Возможные значения и поддерживаемые форматы:
 * - `image` — изображение (JPG, JPEG, PNG, GIF, TIFF, BMP, HEIC). Максимальный
 *   размер одного изображения: до 50 МБ или не более 7680 x 7680 px — должны
 *   выполняться оба критерия
 * - `video` — видеофайл (MP4, MOV, MKV, WEBM). Максимальный размер одного
 *   видео: до 250 МБ
 * - `audio` — аудиофайл (MP3, WAV, M4A и другие). Максимальный размер одного
 *   аудио: до 256 МБ или длительностью не более 60 мин — должны выполняться
 *   оба критерия
 * - `file` — файл, поддерживаются распространённые форматы (например, TXT,
 *   DOC и другие). Максимальный размер одного файла: до 4 ГБ
 *
 * При передаче неподдерживаемого типа файла вернётся ошибка
 * `File extension is forbidden`.
 *
 * Значение `photo` больше не поддерживается. Если вы использовали
 * `type=photo` в ранее созданных интеграциях — замените его на `type=image`
 */
enum UploadType {
  image: "image",
  video: "video",
  audio: "audio",
  file: "file",
}

/** Запрос на прикрепление изображения к сообщению */
model PhotoAttachmentRequest extends AttachmentRequest {
  /** Тип вложения: `image` */
  type: "image";

  /** Полезная нагрузка запроса на прикрепление изображения */
  payload: PhotoAttachmentRequestPayload;
}

/** Запрос на прикрепление видео к сообщению */
model VideoAttachmentRequest extends AttachmentRequest {
  /** Тип вложения: `video` */
  type: "video";

  /** Полезная нагрузка запроса на прикрепление видео */
  payload: UploadedInfo;
}

/** Запрос на прикрепление аудио к сообщению. Должен быть единственным вложением в сообщении */
model AudioAttachmentRequest extends AttachmentRequest {
  /** Тип вложения: `audio` */
  type: "audio";

  /** Полезная нагрузка запроса на прикрепление аудио */
  payload: UploadedInfo;
}

/** Запрос на прикрепление файла к сообщению. Должен быть единственным вложением в сообщении */
model FileAttachmentRequest extends AttachmentRequest {
  /** Тип вложения: `file` */
  type: "file";

  /** Полезная нагрузка запроса на прикрепление файла */
  payload: UploadedInfo;
}

/** Запрос на прикрепление стикера. Должен быть единственным вложением в сообщении */
model StickerAttachmentRequest extends AttachmentRequest {
  /** Тип вложения: `sticker` */
  type: "sticker";

  /** Полезная нагрузка запроса на прикрепление стикера */
  payload: StickerAttachmentRequestPayload;
}

/** Запрос на прикрепление карточки контакта к сообщению. Должен быть единственным вложением в сообщении */
model ContactAttachmentRequest extends AttachmentRequest {
  /** Тип вложения: `contact` */
  type: "contact";

  /** Полезная нагрузка запроса на прикрепление карточки контакта */
  payload: ContactAttachmentRequestPayload;
}

/** Запрос на прикрепление клавиатуры к сообщению */
model InlineKeyboardAttachmentRequest extends AttachmentRequest {
  /** Тип вложения: `inline_keyboard` */
  type: "inline_keyboard";

  /** Полезная нагрузка запроса на прикрепление клавиатуры */
  payload: InlineKeyboardAttachmentRequestPayload;
}

/**
 * Запрос на прикрепление reply-клавиатуры к сообщению.
 *
 * В оригинальной схеме отсутствует в `AttachmentRequest.discriminator.mapping`
 * — см. DEVIATIONS `mapping[reply_keyboard]` в scripts/compare.py (тот же
 * приём, что и для `ChatButton`).
 */
model ReplyKeyboardAttachmentRequest extends AttachmentRequest {
  /** Тип вложения: `reply_keyboard` */
  type: "reply_keyboard";

  /** Применимо только для чатов. Если `true`, клавиатура будет показана только тому, кто упомянул или ответил боту */
  direct?: boolean = false;

  /** Если задано, reply-клавиатура будет показана только этому участнику чата */
  @minValue(0)
  @maxValue(9007199254740991)
  direct_user_id?: int64 | null;

  /** Двумерный массив кнопок. Подробнее о типах кнопок и клавиатуре в ботах — в разделе «Клавиатура для чат-бота» */
  @minItems(1)
  @maxItems(100)
  buttons: ReplyButtonRow[];
}

/** Запрос на прикрепление вложения географического положения */
model LocationAttachmentRequest extends AttachmentRequest {
  /** Тип вложения: `location` */
  type: "location";

  /** Широта. Диапазон от -90 до 90 — требование КБ (числовой тип должен иметь minimum/maximum); в оригинальной схеме MAX границы не заданы */
  @minValue(-90)
  @maxValue(90)
  latitude: float64;

  /** Долгота. Диапазон от -180 до 180 — требование КБ (числовой тип должен иметь minimum/maximum); в оригинальной схеме MAX границы не заданы */
  @minValue(-180)
  @maxValue(180)
  longitude: float64;
}

/** Запрос на прикрепление предпросмотра медиафайла по внешнему URL */
model ShareAttachmentRequest extends AttachmentRequest {
  /** Тип вложения: `share` */
  type: "share";

  /** Полезная нагрузка запроса на прикрепление предпросмотра ссылки */
  payload: ShareAttachmentPayload;
}
