#!/usr/bin/env python3
"""Переходник OpenAPI → JSON Schema draft-07 для quicktype (генерация Go-структур).

quicktype не умеет OpenAPI: его входные языки — json, schema, graphql, postman,
typescript (`quicktype --help`, SRC_LANG). Поэтому components.schemas готового
контракта перекладываются в самостоятельный JSON Schema документ, который и
скармливается генератору. Результат — gen/max.schema.json, промежуточный файл;
его потребитель ровно один — `npm run gen:quicktype`.

Источник — ОДИН документ, openapi.MaxBotApi.yaml. Webhook-документ на 79 из
своих 81 схем дублирует api-документ под префиксом `MaxBotApi.` (разница — только
ButtonRow/ReplyButtonRow, переименованные ButtonRowItem/ReplyButtonRowItem).
Генерация из обоих дала бы 79 типов-близнецов, поэтому из webhook-документа
берутся только две действительно свои схемы — WebhookUpdate и UpdateUnified;
после снятия префикса все их $ref разрешаются в схемы api-документа.

Четыре расхождения OpenAPI 3.0 и JSON Schema, которые приходится закрывать:

1. `additionalProperties: false` — quicktype падает на нём в связке с
   allOf-наследованием: «Can't have non-specified required properties but
   forbidden additionalTypes at #/definitions/Chat» (наследник объявляет
   required: [type], а само свойство приходит из базы через allOf). Печать
   снимается — она нужна валидаторам тела, а не генератору структур.
   ВАЖНО: снимается только запечатывание (false и эквивалентная эмиттеру
   форма {not: {}}); настоящие индексаторы вида
   `additionalProperties: {$ref: SafeInt64}` (Chat.participants) — это
   map[string]T, и их потеря превратила бы карту в interface{}.

2. `nullable: true` (78 позиций) — ключевое слово OpenAPI 3.0, в JSON Schema
   его нет, quicktype его молча игнорирует. Без перевода поле «может быть
   null» стало бы в Go значением, а не указателем. Переводится в
   каноническое JSON Schema «или null»: для узлов с явным type — добавлением
   "null" в список типов, для узлов без него (allOf + nullable) — обёрткой
   в anyOf.

3. `discriminator` — конструкция OpenAPI, quicktype её не читает. Диспетчер
   по update_type/type всё равно пишется руками в потребителе, а mapping
   внутри содержит пути `#/components/schemas/...`, которые незачем тащить
   в JSON Schema. Удаляется.

4. Обёртки `{allOf: [{$ref: X}]}` — 60 из 65 позиций allOf в документе.
   В OpenAPI 3.0 соседи `$ref` игнорируются, поэтому навесить на ссылку
   description или nullable можно только завернув её в allOf-однушку;
   семантически такой узел РАВЕН X. quicktype же разворачивает allOf
   слиянием свойств и на каждой площадке лепит новый тип: схема Message,
   на которую так ссылаются пять раз, давала PinnedMessageClass,
   UpdateMessage, UpdateUnifiedMessage, MessageElement и
   CallbackAnswerMessage вместо одного Message. Обёртки схлопываются
   обратно в голый $ref. Настоящее наследование (allOf + собственные
   properties, 5 позиций на верхнем уровне) не трогаем — там слияние
   и требуется.

5. Плоский набор definitions quicktype обходит только от корня: схемы, до
   которых от корня нет пути, он не именует, а oneOf-варианты схлопывает в
   один общий struct. Поэтому корень — «конверт»: объект со свойством на
   каждую именованную схему. Замер на webhook-документе: без конверта 25
   типов, с конвертом — 138, все варианты Update на месте отдельными
   структурами.
"""
import json
import re
import sys
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parent.parent
API_DOC = ROOT / "openapi.MaxBotApi.yaml"
WEBHOOK_DOC = ROOT / "openapi.MaxBotWebhook.yaml"
OUT = ROOT / "gen" / "max.schema.json"

# Схемы, которые есть только в webhook-документе и не дублируют api-документ.
WEBHOOK_ONLY = ("WebhookUpdate", "UpdateUnified")

NS_PREFIX = "MaxBotApi."
OAS_REF = "#/components/schemas/"
JS_REF = "#/definitions/"

# Ключи OpenAPI, которых нет в JSON Schema и которые quicktype не читает.
DROP_KEYS = ("discriminator", "example-quicktype", "externalDocs", "xml")


def is_sealing(value):
    """Запечатывание object-схемы, а не настоящий индексатор.

    Эмиттер @typespec/openapi3 пишет {not: {}}, scripts/seal_additional_properties.py
    переписывает это на литеральный false — встречаются обе формы.
    """
    return value is False or value == {"not": {}}


# Ключи, допустимые рядом с allOf-однушкой, чтобы считать её обёрткой над $ref,
# а не настоящим наследованием. `type: object` эмиттер ставит на обёртку сам.
WRAPPER_SIBLINGS = {"allOf", "description", "type"}


def collapse_ref_wrapper(out):
    """`{allOf: [{$ref: X}]}` → `{$ref: X}`. См. docstring модуля, п. 4."""
    branches = out.get("allOf")
    if not (isinstance(branches, list) and len(branches) == 1):
        return out
    inner = branches[0]
    if not (isinstance(inner, dict) and set(inner) == {"$ref"}):
        return out
    if not set(out) <= WRAPPER_SIBLINGS:
        return out  # есть свои properties/required — настоящее наследование
    if out.get("type") not in (None, "object"):
        return out
    collapsed = {"$ref": inner["$ref"]}
    if "description" in out:
        collapsed["description"] = out["description"]
    return collapsed


def convert(node):
    """Рекурсивный перевод узла OpenAPI-схемы в JSON Schema draft-07."""
    if isinstance(node, list):
        return [convert(item) for item in node]
    if not isinstance(node, dict):
        return node

    out = {}
    for key, value in node.items():
        if key in DROP_KEYS:
            continue
        if key == "additionalProperties" and is_sealing(value):
            continue
        if key == "nullable":
            continue
        if key == "$ref" and isinstance(value, str):
            out[key] = value.replace(OAS_REF, JS_REF)
            continue
        out[key] = convert(value)

    out = collapse_ref_wrapper(out)

    if node.get("nullable") is not True:
        return out

    # nullable: true → каноническое JSON Schema «или null».
    declared = out.get("type")
    if isinstance(declared, str):
        out["type"] = [declared, "null"]
        return out
    if isinstance(declared, list):
        if "null" not in declared:
            out["type"] = declared + ["null"]
        return out
    # Узел без собственного type (allOf + nullable): обернуть целиком.
    description = out.pop("description", None)
    wrapped = {"anyOf": [out, {"type": "null"}]}
    if description is not None:
        wrapped["description"] = description
    return wrapped


def strip_namespace(node):
    """Снять префикс `MaxBotApi.` с $ref webhook-документа."""
    blob = json.dumps(node, ensure_ascii=False)
    blob = blob.replace(JS_REF + NS_PREFIX, JS_REF)
    return json.loads(blob)


def count_inline_objects(definitions):
    """Вложенные безымянные object-схемы.

    Для каждой такой quicktype выдумывает имя по контексту (Icon, Thumbnail,
    Stat, Link — реальные примеры с webhook-документа). В api-документе их
    ноль: TypeSpec-исходники именуют каждую модель, поэтому подъёма
    инлайн-объектов в definitions здесь не требуется. Счётчик печатается,
    чтобы регрессия была видна в логе сборки, а не в именах Go-типов.
    """
    found = []

    def walk(node, path, top):
        if isinstance(node, dict):
            if not top and "properties" in node:
                found.append(path)
            for key, value in node.items():
                walk(value, f"{path}/{key}", False)
        elif isinstance(node, list):
            for index, value in enumerate(node):
                walk(value, f"{path}/{index}", False)

    for name, schema in definitions.items():
        walk(schema, name, True)
    return found


def load(path):
    if not path.is_file():
        print(f"Нет {path.relative_to(ROOT)} — сначала выполните: npm run build")
        sys.exit(2)
    return yaml.safe_load(path.read_text(encoding="utf-8"))["components"]["schemas"]


def main():
    api = load(API_DOC)
    webhook = load(WEBHOOK_DOC)

    definitions = {name: convert(schema) for name, schema in api.items()}

    for name in WEBHOOK_ONLY:
        if name not in webhook:
            print(f"В {WEBHOOK_DOC.name} нет схемы {name} — контракт изменился, проверьте WEBHOOK_ONLY")
            sys.exit(2)
        if name in definitions:
            print(f"{name} уже есть в {API_DOC.name} — уберите её из WEBHOOK_ONLY")
            sys.exit(2)
        definitions[name] = strip_namespace(convert(webhook[name]))

    dangling = sorted(
        {
            ref
            for ref in re.findall(r'"\$ref": "#/definitions/([^"]+)"', json.dumps(definitions))
            if ref not in definitions
        }
    )
    if dangling:
        print(f"Битые ссылки после сборки definitions: {', '.join(dangling)}")
        sys.exit(2)

    inline = count_inline_objects(definitions)
    if inline:
        print(f"ВНИМАНИЕ: безымянных object-схем — {len(inline)}, quicktype даст им выдуманные имена:")
        for path in inline:
            print(f"  {path}")

    document = {
        "$schema": "http://json-schema.org/draft-07/schema#",
        "title": "MaxBotApi",
        "type": "object",
        # Конверт: по свойству на схему, чтобы каждая получила своё имя. См. docstring, п. 4.
        "properties": {name: {"$ref": JS_REF + name} for name in sorted(definitions)},
        "definitions": definitions,
    }

    OUT.parent.mkdir(parents=True, exist_ok=True)
    OUT.write_text(json.dumps(document, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")

    print(
        f"{OUT.relative_to(ROOT)}: {len(definitions)} схем "
        f"({len(api)} из {API_DOC.name} + {len(WEBHOOK_ONLY)} из {WEBHOOK_DOC.name}), "
        f"безымянных object-схем — {len(inline)}"
    )


if __name__ == "__main__":
    main()
