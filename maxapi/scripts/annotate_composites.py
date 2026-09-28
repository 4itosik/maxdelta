#!/usr/bin/env python3
"""Постобработка сгенерированного OpenAPI: комментарии к композитным схемам.

Замечание [125] SCHEMA-валидатора — «не рекомендуется использовать oneOf,
anyOf, allOf совместно с другими ключами, т.к. композитная часть будет
проигнорирована при генерации» — самое массовое в отчёте (см. review.md).
Расплющивание дискриминированных union'ов
(`scripts/flatten_discriminated_unions.py`) уменьшило число композитных
позиций со 125 до 71 и 59, но обнулить его нельзя: остаток держится
ограничениями самого OpenAPI 3.0. Разбор — в README, раздел «Что остаётся
композитным — и почему это потолок».

Читающий YAML этого README перед глазами не имеет. Скрипт ставит рядом с
каждой оставшейся позицией однострочный комментарий с буквой случая и
причиной, а в начало файла — краткую сводку по случаям с подсчётом позиций.
Пять случаев:

  [A] oneOf + discriminator + description — базы полиморфных полей;
  [B] allOf вокруг одиночного $ref + description — ссылка с описанием;
  [C] то же + nullable — ссылка, принимающая null;
  [D] allOf-наследование без дискриминатора;
  [E] anyOf без соседних ключей — тело вебхука.

Главный аргумент по каждому случаю — что так же сделано в самом контракте
MAX, поэтому у каждого случая печатается пометка «в оригинале» с конкретными
схемами. Цифры и перечень схем для неё берутся из
`reference/max-openapi-official.json`, а не вписаны в текст: если снимок
оригинала обновят, комментарии не разойдутся с ним молча. Без референса
пометки просто не печатаются — лучше их отсутствие, чем непроверенное
утверждение.

Комментарии YAML-парсерами игнорируются, поэтому на потребителей документа
(`kin-openapi` в maxmoc, сверка в `scripts/compare.py`, `validate_kb.py`)
скрипт не влияет никак — только на человека, который откроет файл.

Правки построчные: комментарий вставляется перед строкой ключа с её же
отступом, остальной файл не переформатируется. Запуск идемпотентен —
собственные строки скрипт сначала удаляет, потом расставляет заново, так
что повторный прогон даёт тот же файл.

Место в конвейере — последнее: расплющивание меняет состав композитных
позиций, и комментарии должны описывать уже итоговый документ.

Вторая семья пометок — «# незапечатано» — про требование КБ [59]:
«у object-схемы явные свойства и additionalProperties: false» (см.
review.md). Эмиттер (seal-object-schemas) запечатывает все object-схемы,
кроме форм, где флаг сломал бы семантику: словарь с динамическими ключами
[M], база allOf-наследования [D] и наследник, в котором не перечислены
поля базы [H]. Nullable-обёртки [C] сюда не попадают: type: object, на который
срабатывало правило, у них снимает strip_nullable_wrapper_type.py; если
обёртка с type: object всё же встретилась — этот шаг не отработал, и скрипт
об этом предупреждает. Ключевое ограничение одно:
additionalProperties действует только на properties той же схемы и не
смотрит сквозь allOf. Скрипт находит каждую object-схему без флага, ставит
перед ней строку с буквой случая и причиной, а после шапки про композиты —
вторую шапку с разбором. Позиция, не попавшая ни в один случай, помечается
[?]: это сигнал, что флаг там забыт, а не запрещён.
"""
import json
import re
import sys
import textwrap
from collections import Counter
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parent.parent
OUT_DIR = ROOT / "tsp-output" / "@typespec" / "openapi3"
# Снимок официальной схемы MAX: из него берутся цифры и перечень схем для
# пометок «В ОРИГИНАЛЕ», чтобы они не разъезжались с реальным оригиналом.
REFERENCE = ROOT / "reference" / "max-openapi-official.json"

# Схемы webhook-документа приходят из общего namespace с этим префиксом;
# в официальной схеме его нет.
NS_PREFIX = "MaxBotApi."

COMPOSITE_KEYS = ("oneOf", "anyOf", "allOf")

# Маркеры собственных строк — по ним же они удаляются при повторном прогоне.
# LEGACY_* — маркеры прежней редакции комментариев; нужны только затем, чтобы
# файл, размеченный старым скриптом, тоже вычищался начисто.
SITE_PREFIX = "# композит"
HEADER_OPEN = "# --- oneOf/allOf/anyOf в этом документе: что осталось и почему ---"
HEADER_CLOSE = "# --- конец ---"
LEGACY_SITE_PREFIX = "# КБ[125]"
LEGACY_HEADER_OPEN = "# --- КБ[125]: почему в документе остались oneOf/allOf/anyOf ---"
LEGACY_HEADER_CLOSE = "# --- конец КБ[125] ---"
SEAL_PREFIX = "# незапечатано"
SEAL_HEADER_OPEN = "# --- additionalProperties: false в этом документе: где его нет и почему ---"
SEAL_HEADER_CLOSE = "# --- конец additionalProperties ---"

# Все свои маркеры разом — по ним строки вычищаются при повторном прогоне.
HEADERS = {
    HEADER_OPEN: HEADER_CLOSE,
    SEAL_HEADER_OPEN: SEAL_HEADER_CLOSE,
    LEGACY_HEADER_OPEN: LEGACY_HEADER_CLOSE,
}
SITE_PREFIXES = (SITE_PREFIX, SEAL_PREFIX, LEGACY_SITE_PREFIX)

# Однострочные пояснения у самих позиций.
SITE_NOTE = {
    "A": "полиморфное поле: тот же discriminator, что в оригинале MAX; без oneOf проверялись бы только свойства базы",
    "B": "обёртка одиночного $ref, как в оригинале MAX: в OpenAPI 3.0 соседи $ref игнорируются, описание бы потерялось",
    "C": "обёртка одиночного $ref, как в оригинале MAX: иначе nullable рядом с $ref игнорируется и поле не примет null; type: object снят сознательно (strip_nullable_wrapper_type.py), см. шапку",
    "E": "две равноправные формы тела на выбор разработчика бота; аналога в оригинале нет — вебхук MAX не публикует",
    "?": "композитная схема неизвестного вида — проверьте вручную",
}

# У случая [D] причина зависит от семейства: у одного расплющивание снесло бы
# саму базу, у другого — молча потеряло бы её свойства (см. README). Записаны
# эти позиции ровно так же, как в оригинальной схеме MAX.
INHERITANCE_NOTE = {
    "AttachmentPayload": (
        "наследование от AttachmentPayload через allOf, как в оригинале MAX: общее поле url живёт "
        "в базе, здесь добавляются свои. Других ссылок на AttachmentPayload в контракте нет — "
        "она существует только через эти allOf"
    ),
    "User": (
        "наследование от User через allOf, как в оригинале MAX: 7 полей User (user_id, имя и "
        "остальные) приходят сюда только через allOf, в самой схеме их нет — эмиттер не вписывает "
        "поля базы в схему, от которой наследуют дальше (BotInfo)"
    ),
    "UserWithPhoto": (
        "наследование от UserWithPhoto через allOf, как в оригинале MAX: в UserWithPhoto перечислены "
        "только её 3 собственных поля, 7 полей User доходят до BotInfo только по цепочке allOf"
    ),
}

CASE_TEXT = {
    "A": [
        "[A] oneOf + discriminator + description — {n} поз. Базы полиморфных полей: updates[],",
        "    attachments[], markup[], ряды кнопок, тело вебхука; discriminator и description —",
        "    часть той же конструкции. В оригинале те же шесть дискриминаторов, но варианты",
        "    подключены к базе через allOf: при такой записи у полиморфного поля проверяются",
        "    только свойства базы — из 13 некорректных тел kin-openapi пропускал 11, теперь ноль.",
    ],
    "B": [
        "[B] allOf вокруг одиночного $ref + description — {n} поз. В OpenAPI 3.0 соседи $ref",
        "    игнорируются: без обёртки описание поля пропало бы, а форму {{$ref, description}}",
        "    kin-openapi и Spectral не принимают вовсе. Переход на 3.1 проверен — хуже,",
        "    206 композитных позиций вместо 130.",
        "    В оригинале {orig_b} таких же позиций: Chat.type, Chat.status, Message.sender.",
    ],
    "C": [
        "[C] allOf вокруг одиночного $ref + nullable — {n} поз. То же ограничение 3.0, но на",
        "    кону семантика: без обёртки nullable игнорируется и поле перестаёт принимать null.",
        "    type: object, который эмиттер ставил рядом (по OAS 3.0.3 nullable действует только",
        "    при явном type), снят scripts/strip_nullable_wrapper_type.py: на него срабатывало",
        "    правило КБ [59], а флаг на обёртку поставить нельзя — additionalProperties не смотрит",
        "    сквозь allOf и false отверг бы все поля целевой схемы. Замерено: Go-модели",
        "    oapi-codegen, переходник quicktype и kin-openapi (maxmoc) разницы не видят;",
        "    AJV-валидаторы схему без type не компилируют — это цена решения.",
        "    В оригинале {orig_c} таких позиций (Chat.icon, Chat.pinned_message, Message.link),",
        "    type: object стоит у {orig_c_typed} из них.",
    ],
    "D": [
        "[D] allOf-наследование без дискриминатора — {n} поз. Это не полиморфизм, а обычное",
        "    наследование: база с общими полями и наследник со своими сверху, записано так же,",
        "    как в оригинале MAX. AttachmentPayload держится в контракте только этими allOf, а",
        "    поля User доходят до UserWithPhoto и BotInfo только по цепочке allOf — в самих",
        "    схемах они не перечислены.",
        "@D_EXAMPLES@",
    ],
    "E": [
        "[E] anyOf без соседних ключей — {n} поз. Тело вебхука: на выбор плоская UpdateUnified",
        "    или строгий oneOf WebhookUpdate, посторонних ключей рядом нет. anyOf проходит при",
        "    совпадении любой ветви, поэтому свои исходящие события сверяйте прямо со схемой",
        "    WebhookUpdate. В оригинале аналога нет — webhook-контракт MAX не публикует.",
    ],
    "?": [
        "[?] композитные схемы неизвестного вида — {n} поз. Скрипт их не классифицировал:",
        "    проверьте вручную.",
    ],
}

HEADER_INTRO = [
    "Композитных позиций — {total}; у каждой ниже стоит однострочный комментарий с буквой",
    "случая. Всё оставшееся — либо форма, скопированная из официального контракта MAX",
    "(reference/max-openapi-official.json — снимок schema_2026_08_11.json, API 0.0.33; он же",
    "на dev.max.ru/docs-api/objects/<Имя>), либо ограничение самого OpenAPI 3.0.",
    "Подробный разбор — maxapi/README.md, «Что остаётся композитным — и почему это потолок».",
    "",
]

HEADER_OUTRO = [
    "",
    "Итого: в официальной схеме MAX {orig_total} композитных позиций ({orig_b} [B], {orig_c} [C],",
    "{orig_d} [D]), в этом документе — {total}. Настоящего allOf-наследования почти не осталось:",
    "шесть семейств переписаны в oneOf + discriminator.",
]

# Тот же итог без цифр оригинала — на случай, когда снимка референса нет.
HEADER_OUTRO_PLAIN = [
    "",
    "Настоящего allOf-наследования почти не осталось: шесть семейств переписаны",
    "в oneOf + discriminator.",
]

# Однострочные пояснения у object-схем без additionalProperties: false.
# {base}, {heirs}, {tail} подставляются по месту.
SEAL_NOTE = {
    "C": (
        "nullable-обёртка на {base} с type: object — так быть не должно: его снимает "
        "strip_nullable_wrapper_type.py, проверьте порядок шагов в Makefile; {tail}"
    ),
    "M": (
        "словарь с динамическими ключами, как в оригинале MAX: additionalProperties здесь — "
        "схема значений, false запретил бы любые записи; patternProperties в OpenAPI 3.0 нет"
    ),
    "D": (
        "база allOf-наследования ({heirs}): в allOf каждая ветвь проверяет объект целиком, "
        "false у базы отверг бы собственные поля наследников; {tail}"
    ),
    "H": (
        "наследует {base} через allOf, но поля {base} в самой схеме не перечислены (эмиттер "
        "вписывает их только в схемы, от которых никто не наследует): additionalProperties: false "
        "отверг бы их, потому что не смотрит сквозь allOf"
    ),
    "?": "object-схема без additionalProperties: false, причина не распознана — вероятно, флаг забыт",
}

SEAL_INTRO = [
    "Object-схем без additionalProperties: false — {n} из {objects}; у каждой ниже стоит",
    "однострочный комментарий «# незапечатано» с буквой случая. Требование КБ [59] выполнено",
    "везде, где флаг не ломает семантику; остаток — ограничения JSON Schema и OpenAPI 3.0.",
    "Ключевое: additionalProperties действует только на properties той же схемы и не смотрит",
    "сквозь allOf — на этом держатся случаи [D] и [H]. Nullable-обёртки сюда не попадают:",
    "type: object у них снят, см. [C] в шапке выше.",
    "",
]

# Строки, начинающиеся с «@», заменяются на вычисленный по документу текст.
SEAL_CASE_TEXT = {
    "C": [
        "[C] nullable-обёртка с type: object — {n} поз. Так быть не должно: type: object у таких",
        "    обёрток снимает scripts/strip_nullable_wrapper_type.py; проверьте порядок шагов в",
        "    Makefile.",
        "@C_TARGETS@",
    ],
    "M": [
        "[M] словарь с динамическими ключами {type: object, additionalProperties: {$ref}} — {n} поз.",
        "    Ключи заранее не известны, в properties их не перечислить, patternProperties в",
        "    OpenAPI 3.0 нет; additionalProperties здесь — схема значений, и false сделал бы",
        "    словарь всегда пустым. В оригинале записано так же.",
        "@M_FIELDS@",
    ],
    "D": [
        "[D] базы allOf-наследования — {n} поз. В allOf каждая ветвь проверяет объект целиком,",
        "    и false у базы отверг бы собственные поля наследников. Флаг стоит на наследниках.",
        "    Расплющить наследование и запечатать базы можно, но отвергнуто — README, «Что",
        "    остаётся композитным». Цена: поля, объявленные типом базы (Message.sender: User),",
        "    посторонние ключи не отвергают.",
        "@D_BASES@",
    ],
    "H": [
        "[H] наследник, в котором не перечислены поля базы — {n} поз. Эмиттер вписывает в",
        "    наследника поля базы, только если от него самого никто не наследует; здесь наследники",
        "    были, но их вырезала чистка недостижимых схем. Поля базы приходят только через allOf,",
        "    а additionalProperties: false сквозь allOf не смотрит и отверг бы их.",
        "@H_NAMES@",
    ],
    "?": [
        "[?] object-схемы без флага, не попавшие ни в один случай — {n} поз. Причины скрипт не",
        "    нашёл: проверьте, не забыт ли флаг.",
    ],
}


def node_get(node, key):
    for k, v in node.value:
        if k.value == key:
            return v
    return None


def keys(node):
    return {k.value for k, _ in node.value}


def classify(node):
    """(буква случая, имя базы) для mapping-узла с композитным ключом."""
    keys = {k.value for k, _ in node.value}
    if "anyOf" in keys:
        return "E", None
    if "oneOf" in keys:
        return ("A", None) if "discriminator" in keys else ("?", None)
    allof = next(v for k, v in node.value if k.value == "allOf")
    base = None
    if isinstance(allof, yaml.SequenceNode) and len(allof.value) == 1:
        item = allof.value[0]
        item_keys = {k.value for k, _ in item.value} if isinstance(item, yaml.MappingNode) else set()
        if item_keys == {"$ref"}:
            ref = next(v.value for k, v in item.value if k.value == "$ref")
            base = ref.rsplit("/", 1)[-1]
            # Обёртка ссылки: своих properties у схемы нет.
            if "properties" not in keys:
                return ("C" if "nullable" in keys else "B"), base
    return "D", base


def collect(node, sites):
    """Все композитные позиции файла: (строка ключа, отступ, случай, база)."""
    if isinstance(node, yaml.SequenceNode):
        for item in node.value:
            collect(item, sites)
        return
    if not isinstance(node, yaml.MappingNode):
        return
    for key, value in node.value:
        if key.value in COMPOSITE_KEYS:
            case, base = classify(node)
            sites.append((key.start_mark.line, key.start_mark.column, case, base))
            break
    for _, value in node.value:
        collect(value, sites)


def strip_own(lines):
    """Удаление строк предыдущего прогона: блок шапки и комментарии позиций.

    Маркеры прежней редакции убираются наравне с нынешними — иначе файл,
    размеченный старым скриптом, получил бы вторую шапку поверх первой.
    """
    out, close = [], None
    for line in lines:
        stripped = line.strip()
        if close is None and stripped in HEADERS:
            close = HEADERS[stripped]
            continue
        if close is not None:
            if stripped == close:
                close = None
            continue
        if stripped.startswith(SITE_PREFIXES):
            continue
        out.append(line)
    if close is not None:
        print("Незакрытый блок шапки — файл правили вручную, прерываю")
        sys.exit(2)
    return out


def official():
    """Оригинал MAX: счётчики по случаям и карта «наследник -> база».

    Цифры и перечень схем в пометках «В ОРИГИНАЛЕ» считаются по снимку, а не
    вписаны руками: если референс обновят, комментарии не разойдутся с ним
    молча. Без референса пометки не печатаются вовсе — лучше их отсутствие,
    чем непроверенное утверждение.
    """
    if not REFERENCE.exists():
        return None
    doc = json.loads(REFERENCE.read_text(encoding="utf-8"))
    counts, inherit = Counter(), {}
    seal = Counter()  # ap_false: узлов с additionalProperties: false; c_typed: обёрток [C] с type

    def walk(node, name):
        if isinstance(node, dict):
            keys = set(node)
            if node.get("additionalProperties") is False:
                seal["ap_false"] += 1
            if keys & set(COMPOSITE_KEYS):
                seq = node.get("allOf")
                single_ref = (
                    isinstance(seq, list)
                    and len(seq) == 1
                    and isinstance(seq[0], dict)
                    and set(seq[0]) == {"$ref"}
                )
                if "allOf" not in keys:
                    counts["A" if "discriminator" in keys else "?"] += 1
                elif single_ref and "properties" not in keys:
                    counts["C" if "nullable" in keys else "B"] += 1
                    if "nullable" in keys and "type" in keys:
                        seal["c_typed"] += 1
                else:
                    counts["D"] += 1
                    if name and isinstance(seq, list) and isinstance(seq[0], dict):
                        inherit[name] = seq[0].get("$ref", "").rsplit("/", 1)[-1]
            for k, v in node.items():
                walk(v, name)
        elif isinstance(node, list):
            for v in node:
                walk(v, name)

    for schema_name, schema in doc.get("components", {}).get("schemas", {}).items():
        walk(schema, schema_name)
    walk({k: v for k, v in doc.items() if k != "components"}, None)
    return {
        "counts": counts,
        "inherit": inherit,
        "total": sum(counts.values()),
        "schemas": len(doc.get("components", {}).get("schemas", {})),
        "ap_false": seal["ap_false"],
        "c_typed": seal["c_typed"],
    }


def d_examples(children, off):
    """Строка «в оригинале» для случая [D] — только реально сверенные схемы."""
    names = []
    for name, base in children:
        # В webhook-документе схемы приходят с префиксом namespace-а.
        plain, plain_base = name.removeprefix(NS_PREFIX), (base or "").removeprefix(NS_PREFIX)
        if off["inherit"].get(plain) == plain_base:
            names.append(plain)
    if not names:
        return ["    (в снимке оригинала таких схем не нашлось — проверьте вручную)"]
    text = (
        f"В оригинале ({off['counts']['D']} поз.) те же схемы записаны один в один: "
        + ", ".join(sorted(names))
        + "."
    )
    return textwrap.wrap(text, width=86, initial_indent="    ", subsequent_indent="    ")


def header(counts, total, children, off):
    ctx = {"total": total}
    if off:
        ctx.update(
            orig_total=off["total"],
            orig_b=off["counts"]["B"],
            orig_c=off["counts"]["C"],
            orig_d=off["counts"]["D"],
            orig_c_typed=off["c_typed"],
        )
    body = [t.format(**ctx) for t in HEADER_INTRO]
    for case in ("A", "B", "C", "D", "E", "?"):
        if not counts.get(case):
            continue
        for text in CASE_TEXT[case]:
            if text == "@D_EXAMPLES@":
                if off:
                    body += d_examples(children, off)
                continue
            if not off and "{orig_" in text:
                continue  # без референса цифру подставить не из чего
            body.append(text.format(n=counts[case], **ctx))
        body.append("")
    body = body[:-1] + [t.format(**ctx) for t in (HEADER_OUTRO if off else HEADER_OUTRO_PLAIN)]
    lines = [HEADER_OPEN] + [("# " + t).rstrip() for t in body] + [HEADER_CLOSE]
    return [l + "\n" for l in lines]


def inheritance_children(doc):
    """[(имя схемы, база)] для схем документа, наследующих через allOf."""
    out = []
    for name, schema in doc.get("components", {}).get("schemas", {}).items():
        if not isinstance(schema, dict) or "allOf" not in schema or "properties" not in schema:
            continue
        seq = schema["allOf"]
        if isinstance(seq, list) and seq and isinstance(seq[0], dict):
            out.append((name, seq[0].get("$ref", "").rsplit("/", 1)[-1]))
    return out


def wrap(text):
    return textwrap.wrap(text, width=86, initial_indent="    ", subsequent_indent="    ")


def key_mark(node, key):
    k = next(k for k, _ in node.value if k.value == key)
    return k.start_mark.line, k.start_mark.column


def single_ref(node):
    """Имя схемы из allOf: [$ref] или None."""
    allof = node_get(node, "allOf")
    if not (isinstance(allof, yaml.SequenceNode) and len(allof.value) == 1):
        return None
    item = allof.value[0]
    if not isinstance(item, yaml.MappingNode) or keys(item) != {"$ref"}:
        return None
    return node_get(item, "$ref").value.rsplit("/", 1)[-1]


def collect_unsealed(root, doc):
    """Object-схемы без additionalProperties: false.

    Возвращает ([(строка якоря, отступ, случай, детали)], число object-схем).
    Якорь — ключ, к которому придирается валидатор: `type: object`, а у словаря
    `additionalProperties`. Детали — уже готовый текст пометки и имена для шапки.
    """
    schemas = doc.get("components", {}).get("schemas", {})
    heirs = {}
    for name, base in inheritance_children(doc):
        heirs.setdefault(base, []).append(name)

    def show(name):
        return name.removeprefix(NS_PREFIX)

    def sealed(name):
        s = schemas.get(name)
        return isinstance(s, dict) and s.get("additionalProperties") is False

    components = next((v for k, v in root.value if k.value == "components"), None)
    schemas_node = next((v for k, v in components.value if k.value == "schemas"), None) if components else None
    if schemas_node is None:
        return [], 0

    sites, objects = [], 0

    def classify(node, ks, ap, name, prop, is_root):
        if isinstance(ap, yaml.MappingNode):
            line, col = key_mark(node, "additionalProperties")
            return line, col, "M", {"note": SEAL_NOTE["M"], "field": f"{show(name)}.{prop}"}
        line, col = key_mark(node, "type")
        base = single_ref(node)
        if base and "properties" not in ks:
            tail = f"запечатана сама {show(base)}" if sealed(base) else f"{show(base)} — база наследования, см. пометку у неё"
            note = SEAL_NOTE["C"].format(base=show(base), tail=tail)
            return line, col, "C", {"note": note, "base": show(base)}
        if is_root and name in heirs:
            own = heirs[name]
            open_heirs = [h for h in own if not sealed(h)]
            tail = (
                "запечатаны сами наследники"
                if not open_heirs
                else f"наследник {', '.join(show(h) for h in open_heirs)} сам без флага — см. пометку у него"
            )
            if base:
                tail += f"; сама наследует {show(base)} через allOf, и поля {show(base)} в ней не перечислены — это тоже исключает флаг"
            note = SEAL_NOTE["D"].format(heirs=", ".join(show(h) for h in own), tail=tail)
            return line, col, "D", {"note": note, "name": show(name), "heirs": [show(h) for h in own]}
        if is_root and base and "properties" in ks:
            note = SEAL_NOTE["H"].format(base=show(base))
            return line, col, "H", {"note": note, "name": show(name), "base": show(base)}
        return line, col, "?", {"note": SEAL_NOTE["?"], "name": f"{show(name)}.{prop}" if prop else show(name)}

    def walk(node, name, prop, is_root):
        nonlocal objects
        if isinstance(node, yaml.SequenceNode):
            for item in node.value:
                walk(item, name, prop, False)
            return
        if not isinstance(node, yaml.MappingNode):
            return
        ks = keys(node)
        tnode = node_get(node, "type")
        if isinstance(tnode, yaml.ScalarNode) and tnode.value == "object":
            objects += 1
            ap = node_get(node, "additionalProperties")
            if ap is None or isinstance(ap, yaml.MappingNode):
                sites.append(classify(node, ks, ap, name, prop, is_root))
        for k, v in node.value:
            if k.value == "properties" and isinstance(v, yaml.MappingNode):
                for pk, pv in v.value:
                    walk(pv, name, pk.value, False)
            else:
                walk(v, name, prop, False)

    for name_node, schema_node in schemas_node.value:
        walk(schema_node, name_node.value, None, True)
    return sites, objects


def seal_dynamic(tag, details, off):
    """Строки шапки, вычисляемые по документу (и по снимку оригинала)."""
    if tag == "@C_TARGETS@":
        targets = sorted({d["base"] for d in details})
        return wrap("Свойства и флаг живут в целевой схеме: " + ", ".join(targets) + ".")
    if tag == "@C_ORIGINAL@":
        if not off or not off["counts"]["C"]:
            return []
        return wrap(
            f"В оригинале те же {off['counts']['C']} обёрток (Chat.icon, Chat.pinned_message) "
            f"записаны без флага, type: object стоит у {off['c_typed']} из них."
        )
    if tag == "@M_FIELDS@":
        return wrap("Здесь: " + ", ".join(sorted(d["field"] for d in details)) + ".")
    if tag == "@D_BASES@":
        items = [f"{d['name']} ← {', '.join(d['heirs'])}" for d in sorted(details, key=lambda d: d["name"])]
        return wrap("Здесь: " + "; ".join(items) + ".")
    if tag == "@H_NAMES@":
        items = [f"{d['name']} ← {d['base']}" for d in sorted(details, key=lambda d: d["name"])]
        return wrap("Здесь: " + "; ".join(items) + ".")
    return [f"    {tag}"]


def seal_header(sites, objects, off):
    counts = Counter(case for _, _, case, _ in sites)
    body = [t.replace("{n}", str(len(sites))).replace("{objects}", str(objects)) for t in SEAL_INTRO]
    for case in ("C", "M", "D", "H", "?"):
        if not counts.get(case):
            continue
        details = [d for _, _, c, d in sites if c == case]
        for text in SEAL_CASE_TEXT[case]:
            if text.startswith("@"):
                body += seal_dynamic(text, details, off)
                continue
            body.append(text.replace("{n}", str(counts[case])))
        body.append("")
    if off:
        body.append(
            f"В официальной схеме MAX additionalProperties: false нет ни у одной из {off['schemas']} схем."
            if off["ap_false"] == 0
            else f"В официальной схеме MAX флаг стоит у {off['ap_false']} узлов на {off['schemas']} схем."
        )
    else:
        body = body[:-1]
    lines = [SEAL_HEADER_OPEN] + [("# " + t).rstrip() for t in body] + [SEAL_HEADER_CLOSE]
    return [l + "\n" for l in lines]


def annotate(path, off):
    text = path.read_text(encoding="utf-8")
    lines = strip_own(text.splitlines(keepends=True))

    root = yaml.compose("".join(lines))
    doc = yaml.safe_load("".join(lines))
    sites = []
    collect(root, sites)
    counts = Counter(case for _, _, case, _ in sites)
    children = inheritance_children(doc) if off else []
    unsealed, objects = collect_unsealed(root, doc)

    inserts = []
    for line_no, column, case, base in sites:
        if case == "D":
            # В webhook-документе схемы идут с префиксом namespace-а — текст ищем без него.
            plain = (base or "").removeprefix(NS_PREFIX)
            note = INHERITANCE_NOTE.get(plain, f"наследование от {plain} через allOf")
        else:
            note = SITE_NOTE[case]
        inserts.append((line_no, column, r"^(- )?(oneOf|anyOf|allOf):$", f"{SITE_PREFIX} [{case}] {note}"))
    for line_no, column, case, details in unsealed:
        inserts.append((line_no, column, r"^(type: object|additionalProperties:)$", f"{SEAL_PREFIX} [{case}] {details['note']}"))

    # Вставки идут снизу вверх, иначе съезжают номера строк.
    for line_no, column, expect, text in sorted(inserts, reverse=True):
        target = lines[line_no].strip()
        if not re.match(expect, target):
            # Ключ не открывает собственную строку (flow-стиль) — комментарий
            # встал бы не туда, поэтому позиция пропускается.
            print(f"  {path.name}:{line_no + 1}: пропуск, ключ не в начале строки: {target[:60]}")
            continue
        lines.insert(line_no, f"{' ' * column}{text}\n")

    lines = header(counts, len(sites), children, off) + seal_header(unsealed, objects, off) + lines
    path.write_text("".join(lines), encoding="utf-8")
    return counts, len(sites), Counter(case for _, _, case, _ in unsealed)


def main():
    files = sorted(OUT_DIR.glob("*.yaml"))
    if not files:
        print(f"Нет *.yaml в {OUT_DIR.relative_to(ROOT)} — сначала выполните: npx tsp compile .")
        sys.exit(2)
    off = official()
    if off is None:
        print(f"Нет {REFERENCE.relative_to(ROOT)} — пометки «В ОРИГИНАЛЕ» пропущены")
    total = 0
    for f in files:
        counts, n, unsealed = annotate(f, off)
        by_case = " ".join(f"{c}:{counts[c]}" for c in sorted(counts))
        print(f"{f.name}: {n} композитных позиций прокомментировано ({by_case})")
        seal_by_case = " ".join(f"{c}:{unsealed[c]}" for c in sorted(unsealed))
        print(f"{f.name}: {sum(unsealed.values())} object-схем без additionalProperties: false ({seal_by_case})")
        total += n
        if counts.get("?"):
            print(f"  ВНИМАНИЕ: {counts['?']} позиций не классифицированы")
        if unsealed.get("?"):
            print(f"  ВНИМАНИЕ: {unsealed['?']} object-схем без флага и без причины — флаг забыт?")
        if unsealed.get("C"):
            print(f"  ВНИМАНИЕ: {unsealed['C']} nullable-обёрток с type: object — strip_nullable_wrapper_type.py не отработал?")
    print(f"Итого: {total}")


if __name__ == "__main__":
    main()
