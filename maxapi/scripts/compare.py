#!/usr/bin/env python3
"""Семантическая сверка сгенерированного OpenAPI с официальной схемой Max Bot API.

Сравнивает по сути (пути, методы, operationId, параметры, тела, ответы,
свойства, типы, ограничения), разворачивая $ref и allOf. Имена схем
сравниваются по покрытию components.schemas (имена у нас совпадают с
оригиналом по договорённости). Описания/summary не сравниваются.
"""
import json
import re
import sys
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parent.parent
OFFICIAL = ROOT / "reference" / "max-openapi-official.json"
# Сверяется только основной сервис MaxBotApi; второй файл
# (openapi.MaxBotWebhook.yaml — контракт webhook-сервера разработчика)
# официального аналога не имеет и не сверяется.
OURS = ROOT / "tsp-output" / "@typespec" / "openapi3" / "openapi.MaxBotApi.yaml"

# Осознанные отличия (regex по строке диффа "path: msg", re.search) — см.
# README «Отличия от официальной схемы». Каждый паттерн должен быть максимально
# точечным, чтобы не глушить непредвиденные расхождения в том же поддереве.
DEVIATIONS = [
    r"mapping\[chat\]",  # ChatButton есть в схемах, но отсутствует в Button.discriminator.mapping оригинала (недосмотр)
    # Оригинал не объявляет свойство `type` в properties у ReplyButton (и его
    # наследников по <mapping>-цепочке), хотя дискриминатор задан — в отличие
    # от Button. У нас `type` эмиттится легитимно. В 0.0.33 оригинал начал
    # требовать `type` в required, по-прежнему не объявляя его в properties:
    # половина отклонения закрылась, половина осталась. Ограничено поддеревом
    # schemas.ReplyButton*, чтобы любые другие расхождения (например неверный
    # maxLength у SendContactButton) по-прежнему всплывали как DIFF.
    r"^schemas\.ReplyButton(<[^>]*>)*\.type: лишнее свойство у нас$",
    # DataAttachment есть в схемах и расширяет Attachment, но отсутствует в
    # Attachment.discriminator.mapping оригинала (недосмотр, тот же приём,
    # что и для mapping[chat])
    r"mapping\[data\]",
    # ReplyKeyboardAttachment/ReplyKeyboardAttachmentRequest есть в схемах и
    # расширяют Attachment/AttachmentRequest соответственно, но отсутствуют в
    # соответствующих discriminator.mapping оригинала (недосмотр)
    r"mapping\[reply_keyboard\]",
    # User.username: оригинал объявляет поле обязательным, у нас оно
    # необязательное. Подтверждено данными с прода: в регрессе на живом MAX
    # (см. maxbotdemo/README.md) из 27 снятых объектов User 22 пришли БЕЗ поля
    # username вовсе — оно было только у самого бота. Требование оригинала
    # отвергало бы каждое сообщение от человека без username.
    # ChatMember наследует User, поэтому его required уезжает следом.
    r"required: официально \['first_name', 'is_bot', 'last_activity_time', 'name', 'user_id', 'username'\], у нас \['first_name', 'is_bot', 'last_activity_time', 'name', 'user_id'\]$",
    r"required: официально \['first_name', 'is_admin', 'is_bot', 'is_owner', 'join_time', 'last_access_time', 'last_activity_time', 'name', 'permissions', 'user_id', 'username'\], у нас \['first_name', 'is_admin', 'is_bot', 'is_owner', 'join_time', 'last_access_time', 'last_activity_time', 'name', 'permissions', 'user_id'\]$",
    # NewMessageBody: оригинал требует text, attachments и link, у нас все
    # три необязательны. reference/ снят с dev.max.ru, а официальная
    # спецификация github.com/max-messenger/api-schema (сентябрь 2026)
    # required у этой схемы не содержит вовсе; примеры документации шлют тело
    # без link и attachments, официальный Go-клиент link без ссылки опускает.
    # С required строгий валидатор периметра (Atlassian) отвергал любое
    # сообщение без ссылки: без поля — missing required, с null — null не
    # проходит ветвь allOf nullable-обёртки. Схема достижима через тела
    # POST/PUT /messages и CallbackAnswer.message (POST /answers), поэтому
    # якорь — на сам required-набор, а не на префикс пути.
    r"required: официально \['attachments', 'link', 'text'\], у нас None$",
    # Наследование без allOf: UserWithPhoto, BotInfo, ChatMember и
    # File/Media/StickerAttachmentPayload вписывают поля базы spread'ом, а не
    # extends. Atlassian swagger-request-validator (валидатор периметра) по
    # умолчанию дописывает additionalProperties: false в каждую схему с
    # properties без флага, и ветвь allOf базы отвергала поля наследника —
    # на живом MAX так падали стикеры и GET /me. Сверка allOf разворачивает,
    # поэтому сама форма расхождений не даёт; следствие — пропажа
    # AttachmentPayload, заглушённая выше поимённо.
    #
    # Поля, которых нет ни в оригинале, ни в документации, но которые живой
    # MAX присылает (снято 2026-09-28): fileId во вложении file и id во
    # вложениях audio/video. Схемы запечатаны, и без объявления каждое
    # сообщение с файлом, голосовым или видео отвергалось бы обоими
    # валидаторами — и kin-openapi, и Atlassian.
    r"(^schemas\.FileAttachmentPayload|<file>\.payload)\.fileId: лишнее свойство у нас$",
    r"(^schemas\.MediaAttachmentPayload|<(audio|video)>\.payload)\.id: лишнее свойство у нас$",
    # Событие message_chat_created (MessageChatCreatedUpdate) убрано: его нет
    # ни в официальной спецификации github.com/max-messenger/api-schema, ни в
    # каталоге событий на dev.max.ru, а в оригинале оно не попало даже в
    # Update.discriminator.mapping. Оно единственное тянуло в контракт Chat со
    # словарём participants (additionalProperties: {$ref}), который
    # КБ-валидатор в Jenkins отвергает. Сценарий — создание группового чата
    # кнопкой chat — вне объёма.
    r"^schemas\.MessageChatCreatedUpdate: схема отсутствует у нас$",
    # PhotoAttachmentRequestPayload.photos убран: словарь токенов
    # (additionalProperties: {$ref: PhotoToken}) КБ-валидатор в Jenkins
    # отвергает — у object-схемы должно быть additionalProperties: false.
    # Документация MAX отправляет фото через token или url, а словарь — форма
    # ответа на загрузку: токен из него кладётся в token. PhotoToken после
    # этого ни на что не ссылается и вырезается как недостижимая.
    r"(^schemas\.PhotoAttachmentRequestPayload|<image>\.payload)\.photos: свойства нет у нас$",
    r"^schemas\.PhotoToken: схема отсутствует у нас$",
    # CallbackAnswer.notification есть в официальной спецификации
    # github.com/max-messenger/api-schema, но отсутствует в схеме с dev.max.ru,
    # с которой снят reference/. Без него бот не может ответить на нажатие
    # всплывающим уведомлением, не меняя сообщение.
    r"(^schemas\.CallbackAnswer|^paths\./answers\.post\.requestBody)\.notification: лишнее свойство у нас$",
    # ChatType: у нас строка с ограничениями по форме, у оригинала — закрытый
    # список. Перечисление здесь уже подводило: в 0.0.32 оно состояло из одного
    # "chat", хотя живая платформа отдаёт "dialog" в каждом сообщении диалога —
    # регресс на проде отверг из-за этого все 15 снятых событий. В 0.0.33 набор
    # дополнили до chat/channel/dialog, но способ остался хрупким: следующее
    # добавленное значение снова сломает валидацию у всех потребителей.
    # Подробное обоснование — у самого scalar в models/messages.tsp.
    r"chat_type: enum: официально \['channel', 'chat', 'dialog'\], у нас None$",
    r"chat_type: type: официально None, у нас 'string'$",
    r"^schemas\.ChatType: (enum: официально \['channel', 'chat', 'dialog'\], у нас None|type: официально None, у нас 'string')$",
    r"^schemas\.Chat\.type: (enum: официально \['channel', 'chat', 'dialog'\], у нас None|type: официально None, у нас 'string')$",
    r"\.chat\.type: (enum: официально \['channel', 'chat', 'dialog'\], у нас None|type: официально None, у нас 'string')$",
    # Оригинал непоследователен: VideoAttachment.thumbnail одновременно
    # объявлен как type: "string" И как allOf-ссылка на объектную схему
    # VideoThumbnail ({url: string}) — явно рудимент более ранней версии
    # схемы, где миниатюра была просто URL-строкой. Мы транскрибируем
    # семантически осмысленный вариант (VideoThumbnail, по аналогии с
    # VideoAttachmentDetails.thumbnail: PhotoAttachmentPayload), а не
    # унаследованный обрывок type: string. Тот же приём, что и для
    # ReplyButton/ChatButton — точечный regex по конкретному сообщению диффа.
    # Тот же артефакт достижим и через paths.* (Task 9: любая операция,
    # возвращающая Message/Chat с вложениями, заново раскрывает Attachment,
    # т.к. поле с doc-комментарием эмиттится как allOf-обёртка, а не голый
    # $ref — см. подробное объяснение ниже, у MessageBody). Матчим по
    # суффиксу диффа (без привязки к префиксу schemas./paths.*), т.к. само
    # сообщение диффа уникально для этого артефакта.
    r"attachments\[\](<[^>]*>)*\.thumbnail: type: официально 'string'",
    # MessageBody в оригинале непоследователен в двух местах (недосмотр):
    # 1) required перечисляет 'link', но такого свойства у MessageBody нет
    #    в properties вообще (mid/seq/text/attachments/markup) — явный
    #    рудимент копипасты из Message.required. Мы не эмитируем несуществующее
    #    свойство как required.
    # 2) свойство attachments содержит паразитный вложенный ключ
    #    "required": false прямо внутри схемы самого свойства — это не валидная
    #    конструкция OpenAPI (required — атрибут уровня схемы-объекта, а не
    #    отдельного свойства). У нас такого артефакта нет и быть не может.
    # MessageBody встречается по ссылке в огромном количестве контекстов —
    # и в components.schemas (Message.body, LinkedMessage.message,
    # SendMessageResult.message.*, GetPinnedMessageResult.message.*,
    # Chat.pinned_message.*, все варианты Update.message/.chat.pinned_message
    # — Tasks 7-8), и (Task 9) в paths.* — КАЖДАЯ операция, которая
    # возвращает Chat/Message/MessageList/UpdateList и т.п. своим кодом
    # ответа, заново раскрывает MessageBody с нуля (см. объяснение про
    # allOf-обёртку doc-комментированных свойств: $ref-дедупликация по (na, nb)
    # не срабатывает, т.к. у обёрнутого свойства нет голого "$ref" на верхнем
    # уровне). Перечислять все места-источники (со всеми префиксами schemas.*
    # и paths.*) практически невозможно и хрупко — вместо этого матчим по
    # самому телу diff-сообщения: конкретный required-список ['attachments',
    # 'link', 'mid', 'seq', 'text'] vs ['mid', 'seq', 'text'] и артефакт
    # "attachments: required: официально False" уникальны для MessageBody во
    # всей схеме (никакая другая модель не имеет такого required-набора и не
    # хранит required=false внутри схемы отдельного свойства), поэтому
    # unanchored-паттерн по значению безопасен.
    r"required: официально \['attachments', 'link', 'mid', 'seq', 'text'\], у нас \['mid', 'seq', 'text'\]$",
    r"\.attachments: required: официально False, у нас None$",
    # GET /chats больше не документирован в оригинале (см. подробное
    # обоснование в routes/chats.tsp у операции getChats) — оригинал вообще
    # не содержит ключа "responses" у этой операции. Мы транскрибируем
    # содержательный (исторический) контракт: 200 ChatList, 401, 500 — все
    # три кода закономерно оказываются "лишними" при сравнении с пустым
    # responses оригинала.
    r"^paths\./chats\.get\.responses\[(200|401|500)\]: лишний ответ у нас$",
    # GET /messages: параметр message_ids в оригинале — {type: array, format:
    # string} без объявленного items вообще (тот же приём копипасты, что и у
    # UserIdsList в теле POST /chats/{chatId}/members). Мы транскрибируем
    # содержательный смысл — массив строк с items.
    r"^paths\./messages\.get\.param\(query:message_ids\): items только с одной стороны$",
    # UserIdsList.user_ids: параметро-подобная структура (см. выше про
    # параметро-подобные обёртки в flatten). Раньше посещалась двумя путями:
    # (1) components.schemas.UserIdsList (теперь удалена, т.к. не посещается),
    # (2) paths.* (POST /chats/{chatId}/members requestBody) — это основной путь.
    # Якорен конкретно на этом пути, чтобы не подхватить аналогичные артефакты
    # в других местах (если вообще появятся).
    # Второй путь (schemas.UserIdsList) актуален, пока routes/chats.tsp
    # отключён в main.tsp и схема сравнивается напрямую, а не через paths.*.
    r"^(paths\./chats/\{chatId\}/members\.post\.requestBody|schemas\.UserIdsList)\.user_ids(\[\])?: maxItems: официально (None|100), у нас (100|None)$",
    # ------------------------------------------------------------------
    # Ужесточения по требованиям Кибербезопасности (КБ) — осознанные
    # отличия от официальной схемы, внесённые в TypeSpec-исходники.
    # Обоснования — в doc-комментариях у соответствующих полей *.tsp.
    # Регексы пришпилены к точным значениям наших ограничений, чтобы
    # любое другое расхождение тех же ключей всплывало как DIFF.
    # ------------------------------------------------------------------
    # seal-object-schemas: все object-схемы (кроме расширяемых через allOf
    # базовых) запечатаны через additionalProperties: false. Эмиттер сам
    # пишет эквивалентное `{not: {}}` (не настраивается через tspconfig.yaml —
    # зашито в @typespec/openapi3/schema-emitter.js), поэтому
    # scripts/seal_additional_properties.py переписывает результат
    # `tsp compile` на литеральный `false` перед сверкой.
    r"additionalProperties: официально None, у нас False$",
    # Недостижимые схемы вычищаются из готового YAML
    # (scripts/prune_unused_schemas.py) — требование SCHEMA-валидатора
    # «Potentially unused component». Отсутствуют по двум причинам:
    # 1) обслуживают выключенный набор маршрутов (`// import
    #    "./routes/chats.tsp"` в main.tsp) — сюда же попадают их транзитивные
    #    зависимости (Chat, ChatAdmin, ChatAdminPermission, ChatMember,
    #    ChatStatus, FailedUserDetails, Image, SenderAction, UserWithPhoto),
    #    больше ниоткуда не достижимые — Chat и её зависимости стали такими,
    #    когда убрали событие message_chat_created (см. ниже);
    # 2) сироты самого оригинала, на которые и он ни разу не ссылается —
    #    BotPatch, PhotoTokens;
    # 3) чистая база наследования, чьи поля вписаны в наследников spread'ом
    #    (AttachmentPayload → File/Media/StickerAttachmentPayload): отдельно на
    #    неё никто не ссылается. Почему не extends — см. п. «Наследование без
    #    allOf» ниже.
    # Первопричина остаётся видимой как отдельные расхождения
    # `paths./chats*: путь отсутствует у нас` — здесь глушим только следствие.
    # Список пришпилен поимённо: пропажа любой ДРУГОЙ схемы всплывёт как DIFF.
    r"^schemas\.(ActionRequestBody|BotPatch|Chat|ChatAdmin|ChatAdminPermission"
    r"|ChatAdminsList|ChatList|ChatMember|ChatMembersList|ChatPatch|ChatStatus"
    r"|Image|UserWithPhoto"
    r"|FailedUserDetails|GetPinnedMessageResult|ModifyMembersResult|AttachmentPayload"
    r"|PhotoTokens|PinMessageBody|SenderAction|UserIdsList)"
    r": схема отсутствует у нас$",
    # Числа: явные границы (требование КБ). int64-идентификаторы и
    # unix-время — 0..2^53-1 (JSON-safe диапазон: ID крупнее 2^53 теряли бы
    # точность в JS-клиентах, включая веб-клиент MAX); int32 и счётчики/
    # размеры (width, height, duration, views и т.п.) — 0..2^31-1.
    r"minimum: официально None, у нас 0$",
    r"maximum: официально None, у нас 9007199254740991$",
    r"maximum: официально None, у нас 2147483647$",
    # MessageBody.seq — исключение из «JSON-safe» правила выше: это не счётчик,
    # а упакованное «время создания в мс + счётчик внутри мс», и прод отдаёт
    # ~1.17e17, впятеро с лишним выше 2^53-1 (наблюдение в
    # maxmoc/internal/ids/ids.go). Граница — предел самого int64; выразить её
    # через @maxValue нельзя (JS-числа компилятора теряют точность выше
    # 2^53-1), поэтому её дописывает scripts/apply_int_bounds.py.
    r"\.seq: maximum: официально None, у нас 9223372036854775807$",
    # Географические координаты (LocationAttachment/LocationAttachmentRequest):
    # оригинал объявляет latitude/longitude как number без границ. Диапазоны
    # -90..90 и -180..180 — не наша выдумка, а определение широты/долготы
    # (WGS 84); заодно закрывают то же требование КБ о minimum/maximum.
    r"latitude: minimum: официально None, у нас -90$",
    r"latitude: maximum: официально None, у нас 90$",
    r"longitude: minimum: официально None, у нас -180$",
    r"longitude: maximum: официально None, у нас 180$",
    # Строковые поля: maxLength. Значения по классам полей: 255 — дефолт
    # для строк без явного размера (включая message_id/mid и — временно,
    # пока не используется, — транскрипцию аудио); 64 — типы
    # вложений/событий/кнопок, BotPatch.name; 200/400 — название/описание
    # чата (зеркала официальных chat_title/chat_description); 512 —
    # start_payload (зеркало ChatButton.start_payload); 1024 — токены и
    # payload (зеркало кнопочного payload); 2048 — URL; 4000 — текст
    # сообщения (зеркало NewMessageBody.text); 4096 — VCF-карточки.
    r"maxLength: официально None, у нас (64|200|255|400|512|1024|2048|4000|4096)$",
    # Массивы: maxItems (требование КБ). Значения: 20 — списки прав
    # (в enum 13 значений); 50 — типы событий, админы (документированный
    # максимум); 100 — вложения, кнопки в ряду/рядов, страницы пагинации,
    # списки пользователей; 500 — элементы разметки; 1000 — updates
    # (зеркало максимума параметра limit).
    r"maxItems: официально None, у нас (20|50|100|500|1000)$",
    # Паттерны: тип вложения/события ([a-z_]), https-URL вебхука,
    # message_id (mid — реальные ID содержат точку), videoToken (паттерн
    # оригинала не заякорен и пропускал любую строку; заякорен и расширен
    # до типовых алфавитов токенов base64/base64url/hex).
    r"pattern: официально None, у нас '\^\[a-z_\]\+\$'$",
    r"pattern: официально None, у нас '\^https://\.\+\$'$",
    r"pattern: официально None, у нас '\^\[a-zA-Z0-9\._\\\\-\]\+\$'$",
    r"pattern: официально '\[a-zA-Z0-9_\\\\-\]\+', у нас '\^\[a-zA-Z0-9\+/=\._\\\\-\]\+\$'$",
    # minLength 1 приносят scalar-типы MessageId/UpdateType.
    r"minLength: официально None, у нас 1$",
    # КБ-идиома «любой текст заданной длины»: требование КБ — pattern у
    # КАЖДОГО строкового поля. Там, где у поля нет содержательной структуры
    # (свободный текст, имена, описания, токены, URL, локали), паттерн
    # выражает ровно то же, что и maxLength, не сужая множество допустимых
    # значений: `[\w\W]` — объединение класса и его дополнения, т.е. любой
    # символ, включая перевод строки. Осознанно НЕ ужесточаем алфавит: схема
    # транскрибирует чужой сервер, реальное множество значений (юникод в
    # именах, произвольные CDN-URL, непрозрачные токены) нам не подконтрольно,
    # и слишком узкий pattern отвергал бы валидные ответы MAX.
    # Нижняя граница берётся из minLength поля (0 или 1), верхняя — из
    # maxLength, поэтому паттерн всегда согласован с соседними ограничениями.
    r"pattern: официально None, у нас '\^\[\\\\w\\\\W\]\{[01],\d+\}\$'$",
    # callback_id: паттерн оригинала использует lookahead (не поддерживается
    # RE2/СОВА) и пропускает любое непустое значение — заменён на «любые
    # непробельные символы».
    r"^paths\./answers\.post\.param\(query:callback_id\): pattern: официально '\^\(\?!\\\\s\*\$\)\.\+', у нас '\^\\\\S\{1,1024\}\$'$",
]
# Схемы оригинала, которые мы намеренно не воспроизводим
NAME_ALLOW_MISSING = {"bigint"}

CONSTRAINTS = [
    "type", "format", "enum", "required", "nullable", "default",
    "maxLength", "minLength", "maximum", "minimum",
    "maxItems", "minItems", "pattern",
]

diffs = []

# Полиморфизм описан в двух разных формах, см. cmp_schema():
#   ALLOF_VARIANTS — имена наследников дискриминаторных баз оригинала
#                    (форма «база-объект + allOf-ссылка на неё у наследника»);
#   ONEOF_VARIANTS — имена вариантов наших дискриминированных union'ов
#                    (каноническая форма «oneOf + discriminator» на базе,
#                    её делает scripts/flatten_discriminated_unions.py).
# Заполняются в main() по загруженным документам. На нерасплющенной схеме
# ONEOF_VARIANTS пуст — вся связанная с ним логика сверки не срабатывает.
ALLOF_VARIANTS = set()
ONEOF_VARIANTS = set()


def report(path, msg):
    diffs.append(f"{path}: {msg}")


def load(p):
    with open(p) as f:
        if p.suffix in (".yaml", ".yml"):
            return yaml.safe_load(f)
        return json.load(f)


def deref(s, root):
    while isinstance(s, dict) and "$ref" in s:
        node = root
        for part in s["$ref"].lstrip("#/").split("/"):
            node = node[part]
        s = node
    return s


def flatten(s, root):
    """deref + слияние allOf в один уровень + чистка параметро-подобных обёрток."""
    s = deref(s, root)
    if not isinstance(s, dict):
        return {}
    if "schema" in s and not ({"type", "properties", "allOf", "enum", "items"} & set(s)):
        s = deref(s["schema"], root)  # артефакт оригинала (UserIdsList)
    if "allOf" not in s:
        return s
    merged, props, req = {}, {}, set()
    parts = [flatten(p, root) for p in s["allOf"]]
    parts.append({k: v for k, v in s.items() if k != "allOf"})
    for p in parts:
        for pk, pv in p.get("properties", {}).items():
            # seal-object-schemas добавляет наследникам пустые стабы ({})
            # унаследованных свойств, чтобы additionalProperties их не
            # отсекал; при слиянии allOf стаб не должен затирать
            # типизированное объявление из базовой схемы.
            if pv == {} and pk in props:
                continue
            props[pk] = pv
        req |= set(p.get("required", []))
        for k, v in p.items():
            if k not in ("properties", "required"):
                merged[k] = v
    if props:
        merged["properties"] = props
    if req:
        merged["required"] = sorted(req)
    return merged


def ref_name(ref):
    """Имя схемы из строки `#/components/schemas/<Имя>` (или None)."""
    return ref.rsplit("/", 1)[-1] if isinstance(ref, str) else None


def is_disc_union(s):
    """Дискриминированный union в канонической форме OpenAPI: oneOf + discriminator."""
    return isinstance(s, dict) and "oneOf" in s and "discriminator" in s


def union_refs(s):
    """Имена схем-вариантов из oneOf."""
    return [ref_name(p["$ref"]) for p in s.get("oneOf") or []
            if isinstance(p, dict) and "$ref" in p]


def mapping_refs(s):
    """Имена схем, на которые указывает discriminator.mapping.

    Значения mapping бывают и строкой-$ref, и объектом {"$ref": ...} —
    см. нормализацию в cmp_schema().
    """
    mapping = (s.get("discriminator") or {}).get("mapping") or {}
    return [ref_name(v["$ref"] if isinstance(v, dict) else v) for v in mapping.values()]


def allof_variant_names(doc):
    """Наследники дискриминаторных баз (форма оригинала: allOf-ссылка на базу)."""
    schemas = (doc.get("components") or {}).get("schemas") or {}
    bases = {n for n, s in schemas.items() if isinstance(s, dict) and "discriminator" in s}
    return {n for n, s in schemas.items() if isinstance(s, dict)
            and any(isinstance(p, dict) and ref_name(p.get("$ref")) in bases
                    for p in (s.get("allOf") or []))}


def oneof_variant_names(doc):
    """Варианты наших дискриминированных union'ов (форма oneOf + discriminator)."""
    schemas = (doc.get("components") or {}).get("schemas") or {}
    out = set()
    for s in schemas.values():
        if is_disc_union(s):
            out.update(n for n in union_refs(s) if n)
    return out


def norm_int(v):
    if isinstance(v, str) and v.lstrip("-").isdigit():
        return int(v)
    return v


def constraints_of(s):
    c = {}
    for k in CONSTRAINTS:
        if k in s:
            v = norm_int(s[k])
            c[k] = sorted(v) if isinstance(v, list) else v
    # нормализация артефактов оригинала:
    if c.get("type") == "integer":
        c.pop("pattern", None)          # pattern на числах не имеет смысла
    if c.get("type") == "array":
        if "minLength" in c:
            c["minItems"] = c.pop("minLength")   # кнопки клавиатур
        if c.get("format") == "string":
            c.pop("format")             # GET /messages message_ids
    if "enum" in c:
        c.pop("type", None)             # enum-схемы оригинала без type: string
    if c.get("nullable") is False:
        c.pop("nullable")
    if c.get("type") == "object":
        c.pop("type")               # эмиттер всегда пишет type: object, оригинал — почти никогда
    if "required" in c and isinstance(c["required"], list):
        c["required"] = sorted(set(c["required"]))
    return c


def cmp_schema(a, b, ra, rb, path, seen, is_disc_prop=False):
    na = a.get("$ref") if isinstance(a, dict) else None
    nb = b.get("$ref") if isinstance(b, dict) else None
    # Пара «вариант дискриминированного union'а»: слева наследник базы через
    # allOf (форма оригинала), справа — участник oneOf (наша форма). У такой
    # пары НЕЛЬЗЯ сравнивать discriminator: в форме оригинала он затекает в
    # наследника через allOf (flatten() сливает ключи базы в наследника), а в
    # нашей остаётся только на базе — там он и сверяется. Иначе каждый вариант
    # даёт ложное `discriminator: X vs None` плюс всю таблицу mapping целиком —
    # ~700 расхождений на пустом месте.
    # Оба варианта сюда попадают по голому $ref: и из цикла по
    # components.schemas, и из обхода discriminator.mapping базы.
    variant_pair = ref_name(na) in ALLOF_VARIANTS and ref_name(nb) in ONEOF_VARIANTS
    if na or nb:
        key = (na, nb)
        if key in seen:
            return
        seen.add(key)
    a, b = flatten(a, ra), flatten(b, rb)
    # То же расхождение форм на уровне самой базы: у нас `oneOf` + discriminator,
    # у оригинала — объект со свойствами базы + discriminator. Собственных
    # `properties`/`required` у oneOf-базы нет и быть не может: они
    # продублированы в КАЖДОМ варианте (scripts/fill_inherited_stubs.py +
    # scripts/flatten_discriminated_unions.py) и сверяются повариантно, где
    # flatten() подмешивает свойства базы оригинала в её наследника. Поэтому
    # здесь сравниваем только состав вариантов и сам дискриминатор — потеря
    # покрытия нулевая: пропажа любого свойства базы всплывёт на каждом из
    # вариантов.
    union_pair = is_disc_union(b) and not is_disc_union(a)
    ca, cb = constraints_of(a), constraints_of(b)
    if union_pair:
        for c in (ca, cb):
            c.pop("required", None)
            c.pop("type", None)
    # Дискриминатор-литералы: TypeSpec `type: "callback"` компилируется в
    # {type: string, enum: [callback]} на наследнике; оригинал почти всегда
    # просто наследует нетипизированное строковое поле базовой схемы, не
    # переобъявляя его с enum. Семантически это одно и то же (значение
    # дискриминатора фиксировано через discriminator.mapping), поэтому не
    # считаем расхождением. Применяем нормализацию ТОЛЬКО к самому полю
    # дискриминатора (is_disc_prop), а не к любому string-полю с одиночным
    # enum — иначе случайно захардкоженный enum на обычном свойстве (напр.
    # "status") будет молча проглочен вместо DIFF.
    if (is_disc_prop
            and a.get("type") == "string" and "enum" not in a
            and b.get("type") == "string"
            and isinstance(b.get("enum"), list) and len(b["enum"]) == 1):
        ca.pop("type", None)
        cb.pop("type", None)
        cb.pop("enum", None)
    for k in sorted(set(ca) | set(cb)):
        if ca.get(k) != cb.get(k):
            report(path, f"{k}: официально {ca.get(k)!r}, у нас {cb.get(k)!r}")
    pa, pb = a.get("properties", {}), b.get("properties", {})
    # propertyName дискриминатора текущей (родительской) схемы; flatten()
    # протаскивает discriminator базовой схемы в наследников через allOf,
    # так что это доступно и на развёрнутых дочерних схемах.
    disc_prop = (a.get("discriminator") or {}).get("propertyName") or (b.get("discriminator") or {}).get("propertyName")
    for k in () if union_pair else sorted(set(pa) | set(pb)):
        if k not in pb:
            report(f"{path}.{k}", "свойства нет у нас")
        elif k not in pa:
            report(f"{path}.{k}", "лишнее свойство у нас")
        else:
            cmp_schema(pa[k], pb[k], ra, rb, f"{path}.{k}", seen, is_disc_prop=(k == disc_prop))
    if ("items" in a) != ("items" in b):
        report(path, "items только с одной стороны")
    elif "items" in a:
        cmp_schema(a["items"], b["items"], ra, rb, f"{path}[]", seen)
    ap, bp = a.get("additionalProperties"), b.get("additionalProperties")
    if isinstance(ap, dict) and isinstance(bp, dict):
        cmp_schema(ap, bp, ra, rb, f"{path}{{}}", seen)
    elif ap != bp:
        report(path, f"additionalProperties: официально {ap!r}, у нас {bp!r}")
    if union_pair:
        # Состав oneOf обязан совпадать с discriminator.mapping: mapping сам
        # сверяется с оригиналом ниже, и эта проверка замыкает цепочку
        # «наследники оригинала -> его mapping -> наш mapping -> наш oneOf».
        # Без неё потерянный или лишний вариант в oneOf прошёл бы незамеченным.
        refs, mapped = set(union_refs(b)), set(mapping_refs(b))
        for n in sorted(refs ^ mapped):
            where = "oneOf" if n in refs else "discriminator.mapping"
            report(path, f"вариант {n} есть только в {where}")
    if variant_pair:
        return
    da = a.get("discriminator", {})
    db = b.get("discriminator", {})
    if da.get("propertyName") != db.get("propertyName"):
        report(path, f"discriminator: {da.get('propertyName')} vs {db.get('propertyName')}")
    ma, mb = da.get("mapping", {}), db.get("mapping", {})
    for k in sorted(set(ma) ^ set(mb)):
        report(path, f"discriminator.mapping[{k}] только с одной стороны")
    for k in sorted(set(ma) & set(mb)):
        # Оригинал непоследователен: mapping-значения обычно строки-$ref
        # ("#/components/schemas/Foo"), но у Update это вложенные объекты
        # {"$ref": "..."} (см. Update.discriminator.mapping). Нормализуем
        # обе формы к строке-ref, чтобы сравнение не падало и работало
        # одинаково независимо от того, какую форму выбрал оригинал.
        va = ma[k]["$ref"] if isinstance(ma[k], dict) else ma[k]
        vb = mb[k]["$ref"] if isinstance(mb[k], dict) else mb[k]
        cmp_schema({"$ref": va}, {"$ref": vb}, ra, rb, f"{path}<{k}>", seen)


def cmp_content(a, b, ra, rb, path, seen):
    sa = (a or {}).get("content", {}).get("application/json", {}).get("schema")
    sb = (b or {}).get("content", {}).get("application/json", {}).get("schema")
    if (sa is None) != (sb is None):
        report(path, f"тело только с одной стороны (официально: {sa is not None})")
    elif sa is not None:
        cmp_schema(sa, sb, ra, rb, path, seen)


def main():
    if not OURS.exists():
        print(f"Нет {OURS.relative_to(ROOT)} — сначала выполните: npx tsp compile .")
        sys.exit(2)
    off, ours = load(OFFICIAL), load(OURS)
    ALLOF_VARIANTS.update(allof_variant_names(off))
    ONEOF_VARIANTS.update(oneof_variant_names(ours))
    seen = set()

    # 1. Пути и операции
    po, pu = off.get("paths", {}), ours.get("paths", {})
    for p in sorted(set(po) | set(pu)):
        if p not in pu:
            report(f"paths.{p}", "путь отсутствует у нас")
            continue
        if p not in po:
            report(f"paths.{p}", "лишний путь у нас")
            continue
        mo = {m: v for m, v in po[p].items() if m in ("get", "post", "put", "patch", "delete")}
        mu = {m: v for m, v in pu[p].items() if m in ("get", "post", "put", "patch", "delete")}
        for m in sorted(set(mo) | set(mu)):
            path = f"paths.{p}.{m}"
            if m not in mu:
                report(path, "операция отсутствует у нас")
                continue
            if m not in mo:
                report(path, "лишняя операция у нас")
                continue
            a, b = mo[m], mu[m]
            if a.get("operationId") != b.get("operationId"):
                report(path, f"operationId: {a.get('operationId')} vs {b.get('operationId')}")
            # Официальные теги обязаны присутствовать у нас; дополнительные
            # наши теги (сквозные группировки вроде bot-sending) допустимы.
            if not set(a.get("tags", [])) <= set(b.get("tags", [])):
                report(path, f"tags: {a.get('tags')} vs {b.get('tags')}")
            qa = {(x["name"], x["in"]): x for x in (deref(x, off) for x in a.get("parameters", []))}
            qb = {(x["name"], x["in"]): x for x in (deref(x, ours) for x in b.get("parameters", []))}
            for key in sorted(set(qa) | set(qb)):
                ppath = f"{path}.param({key[1]}:{key[0]})"
                if key not in qb:
                    report(ppath, "параметра нет у нас")
                elif key not in qa:
                    report(ppath, "лишний параметр у нас")
                else:
                    if bool(qa[key].get("required")) != bool(qb[key].get("required")):
                        report(ppath, "required не совпадает")
                    cmp_schema(qa[key].get("schema", {}), qb[key].get("schema", {}),
                               off, ours, ppath, seen)
            ba, bb = a.get("requestBody"), b.get("requestBody")
            if (ba is None) != (bb is None):
                report(f"{path}.requestBody", f"тело только с одной стороны (официально: {ba is not None})")
            elif ba is not None:
                cmp_content(deref(ba, off), deref(bb, ours), off, ours, f"{path}.requestBody", seen)
            for code in sorted(set(a.get("responses", {})) | set(b.get("responses", {}))):
                rpath = f"{path}.responses[{code}]"
                if code not in b.get("responses", {}):
                    report(rpath, "ответа нет у нас")
                elif code not in a.get("responses", {}):
                    report(rpath, "лишний ответ у нас")
                else:
                    cmp_content(deref(a["responses"][code], off), deref(b["responses"][code], ours),
                                off, ours, rpath, seen)

    # 2. Покрытие components.schemas по именам + структурная сверка одноимённых
    so = set(off["components"]["schemas"]) - NAME_ALLOW_MISSING
    su = set(ours.get("components", {}).get("schemas", {}))
    for n in sorted(so - su):
        report(f"schemas.{n}", "схема отсутствует у нас")
    for n in sorted(so & su):
        cmp_schema({"$ref": f"#/components/schemas/{n}"}, {"$ref": f"#/components/schemas/{n}"},
                   off, ours, f"schemas.{n}", seen)

    known = [d for d in diffs if any(re.search(p, d) for p in DEVIATIONS)]
    real = [d for d in diffs if d not in known]
    for d in real:
        print(f"DIFF  {d}")
    for d in known:
        print(f"KNOWN {d}")
    print(f"\nИтого: {len(real)} расхождений, {len(known)} известных отличий")
    sys.exit(1 if real else 0)


if __name__ == "__main__":
    main()
