#!/usr/bin/env python3
"""Постобработка сгенерированного OpenAPI: allOf-наследование -> oneOf + discriminator.

TypeSpec выражает полиморфизм наследованием моделей, и эмиттер
@typespec/openapi3 переносит его в OpenAPI буквально: базовая схема остаётся
объектом со своими свойствами и `discriminator`, а каждый наследник ссылается
на неё через `allOf`. Скрипт переписывает такое семейство в каноническую для
OpenAPI форму дискриминированного union'а:

    БЫЛО                                СТАЛО
    Update:            {type: object,   Update:            {oneOf: [...15 ссылок],
                        properties,                         discriminator,
                        required,                           description}
                        discriminator,
                        description}
    BotStartedUpdate:  {type: object,   BotStartedUpdate:  {type: object,
                        properties,                         properties,
                        required: свой,                     required: свой + базы,
                        additionalProperties: false,        additionalProperties: false,
                        allOf: [$ref Update],               description}
                        description}

Зачем это нужно — три причины, по возрастанию важности.

1. Требование КБ-валидатора «не использовать oneOf/anyOf/allOf совместно с
   другими ключами»: каждый наследник — это `allOf` рядом с `properties`,
   `required` и `additionalProperties`. После расплющивания таких схем в
   документе не остаётся вовсе (остаются только обёртки `allOf: [$ref]` +
   `description` — одиночная ссылка с описанием иначе в OpenAPI 3.0
   невыразима, потому что `$ref` рядом с другими ключами игнорируется; ровно
   столько же таких обёрток содержит и официальная схема MAX).

2. Требование КБ «у object-схемы должны быть явные свойства и
   `additionalProperties: false`»: базовая схема-объект запечатана быть не
   может — иначе она отсекала бы поля собственных наследников. В форме
   `oneOf` база перестаёт быть object-схемой, и требование к ней неприменимо.

3. Главное: `allOf`-наследование не проверяется валидаторами тела.
   `kin-openapi` (и не он один) не использует `discriminator` голой
   object-схемы, чтобы перейти к конкретному варианту, — а именно против базы
   валидируются полиморфные поля (`updates: Update[]`,
   `attachments: AttachmentRequest[]`). Проверяются только свойства самой
   базы, у `Attachment` это один `type`. Измерено на maxmoc: `Update` с
   полем, которого нет в контракте, и `location`-вложение с `latitude: 991`
   до расплющивания проходят валидацию, после — отвергаются. То есть
   ограничения, навешенные на конкретные варианты, для полиморфных полей
   сегодня просто не работают.

Файл при этом становится КОРОЧЕ, а не длиннее: `scripts/fill_inherited_stubs.py`
уже вкопировал в каждого наследника полные объявления всех унаследованных
свойств, поэтому удаление `allOf` — чистый минус строк. Единственное, чего в
наследниках не хватает, — `required` базы (эмиттер не дублирует его, полагаясь
на пересечение через `allOf`); его скрипт дописывает.

Порядок вариантов в `oneOf` берётся из `discriminator.mapping`, то есть из
порядка объявления моделей в TypeSpec.

Вторым проходом у схем, которые уже пришли из TypeSpec в форме `oneOf`
(`union` с `@discriminated` — в нашей схеме это `WebhookUpdate`), снимается
избыточный ключ `type: object`: эмиттер проставляет его рядом с `oneOf`, что
и бессмысленно (тип задаёт каждый вариант), и нарушает то самое требование КБ
о композитных схемах. В итоге все дискриминированные union'ы документа
выглядят одинаково — `oneOf` + `discriminator` + `description`.

Правки построчные — форматирование остального файла не трогается.
Запуск идемпотентен: у уже расплющенной базы нет ни `allOf`-наследников, ни
собственных `properties`, и она пропускается.
"""
import sys
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parent.parent
OUT_DIR = ROOT / "tsp-output" / "@typespec" / "openapi3"

REF_PREFIX = "#/components/schemas/"

# Ключи базы, которые заменяются на oneOf: собственный контракт базы целиком
# переезжает в варианты (там он уже продублирован — см. docstring).
BASE_KEYS_DROPPED = ("type", "required", "properties")


def fail(msg):
    """Предохранитель: любое неожиданное расположение — отказ, а не тихая порча."""
    print(f"ОТКАЗ: {msg}")
    sys.exit(2)


def ref_name(ref):
    return ref[len(REF_PREFIX):] if isinstance(ref, str) and ref.startswith(REF_PREFIX) else None


def allof_refs(schema):
    """Имена схем, на которые ссылается allOf (None для не-$ref частей)."""
    return [ref_name(p.get("$ref")) if isinstance(p, dict) else None
            for p in schema.get("allOf") or []]


def mapping_order(base):
    """Имена вариантов в порядке discriminator.mapping."""
    mapping = (base.get("discriminator") or {}).get("mapping") or {}
    return [ref_name(v["$ref"] if isinstance(v, dict) else v) for v in mapping.values()]


def schema_nodes(root):
    """{имя схемы: (первая строка, строка за последней, узел)} — 0-индексно."""
    components = next((v for k, v in root.value if k.value == "components"), None)
    if components is None:
        return {}
    schemas = next((v for k, v in components.value if k.value == "schemas"), None)
    if schemas is None:
        return {}
    starts = [(k.value, k.start_mark.line, v) for k, v in schemas.value]
    bounds = [ln for _, ln, _ in starts[1:]] + [schemas.end_mark.line]
    return {name: (ln, end, node) for (name, ln, node), end in zip(starts, bounds)}


def key_spans(node, schema_end):
    """{ключ схемы: (строка ключа, строка за концом его значения, отступ)}."""
    ks = [(k.value, k.start_mark.line, k.start_mark.column) for k, v in node.value]
    bounds = [ln for _, ln, _ in ks[1:]] + [schema_end]
    return {k: (ln, end, col) for (k, ln, col), end in zip(ks, bounds)}


def families(schemas):
    """{база: [варианты]} для дискриминаторных баз, ещё не расплющенных."""
    out = {}
    for name, s in schemas.items():
        if not isinstance(s, dict) or "discriminator" not in s or "oneOf" in s:
            continue
        children = [n for n, c in schemas.items()
                    if isinstance(c, dict) and name in allof_refs(c)]
        if not children:
            continue                      # база без наследников — не трогаем
        out[name] = children
    return out


def check(schemas, base_name, children):
    """Предохранители: расплющивание должно быть строго эквивалентным."""
    base = schemas[base_name]
    if allof_refs(base):
        fail(f"{base_name}: база сама наследует через allOf — многоуровневые "
             f"дискриминированные иерархии не поддерживаются")
    base_props = base.get("properties") or {}
    if not base_props:
        fail(f"{base_name}: у базы нет properties — нечего переносить в варианты")
    ordered = mapping_order(base)
    if sorted(filter(None, ordered)) != sorted(children):
        fail(f"{base_name}: discriminator.mapping ({sorted(filter(None, ordered))}) "
             f"не совпадает с набором наследников ({sorted(children)})")
    for name in children:
        child = schemas[name]
        refs = allof_refs(child)
        if refs != [base_name]:
            fail(f"{name}: allOf содержит не только ссылку на {base_name} ({refs}) — "
                 f"расплющивание изменило бы смысл схемы")
        own = child.get("properties") or {}
        missing = [p for p in base_props if p not in own]
        if missing:
            fail(f"{name}: не объявлены унаследованные свойства {missing} — "
                 f"после удаления allOf они пропали бы из контракта "
                 f"(их должен был вкопировать scripts/fill_inherited_stubs.py)")
    return ordered


def redundant_type_keys(schemas):
    """Схемы с `oneOf` и лишним `type: object` рядом (эмиттер union'ов)."""
    return [n for n, s in schemas.items()
            if isinstance(s, dict) and "oneOf" in s and s.get("type") == "object"]


def process(path):
    text = path.read_text(encoding="utf-8")
    doc = yaml.safe_load(text)
    schemas = (doc.get("components") or {}).get("schemas") or {}
    fams = families(schemas)
    stray = redundant_type_keys(schemas)
    if not fams and not stray:
        return 0, 0, []

    spans = schema_nodes(yaml.compose(text))
    lines = text.split("\n")
    edits = []          # (первая строка, строка за последней, новые строки)
    report = []
    variants = 0

    for base_name in sorted(fams):
        children = fams[base_name]
        ordered = check(schemas, base_name, children)
        base = schemas[base_name]
        base_required = list(base.get("required") or [])

        # --- варианты: убрать allOf, дописать недостающий required базы
        for name in children:
            child = schemas[name]
            start, end, node = spans[name]
            ks = key_spans(node, end)
            a_start, a_end, _ = ks["allOf"]
            edits.append((a_start, a_end, []))
            missing = [r for r in base_required if r not in (child.get("required") or [])]
            if not missing:
                continue
            if "required" in ks:
                r_start, r_end, r_col = ks["required"]
                edits.append((r_end, r_end, [" " * (r_col + 2) + f"- {r}" for r in missing]))
            else:
                # required целиком отсутствует — заводим блок перед properties
                p_start, _, p_col = ks.get("properties") or next(iter(ks.values()))
                edits.append((p_start, p_start,
                              [" " * p_col + "required:"]
                              + [" " * (p_col + 2) + f"- {r}" for r in missing]))

        # --- база: type/required/properties -> oneOf со ссылками на варианты
        b_start, b_end, b_node = spans[base_name]
        bks = key_spans(b_node, b_end)
        dropped = [bks[k] for k in BASE_KEYS_DROPPED if k in bks]
        col = min(c for _, _, c in dropped)
        first = min(s for s, _, _ in dropped)
        block = [" " * col + "oneOf:"] + [
            " " * (col + 2) + f"- $ref: '{REF_PREFIX}{n}'" for n in ordered]
        for start, end, _ in dropped:
            edits.append((start, end, block if start == first else []))

        variants += len(children)
        report.append(f"  {path.name}: {base_name} -> oneOf из {len(children)} вариантов")

    # --- union'ы, пришедшие из TypeSpec уже в форме oneOf: снять `type: object`
    for name in sorted(stray):
        start, end, node = spans[name]
        t_start, t_end, _ = key_spans(node, end)["type"]
        edits.append((t_start, t_end, []))
        report.append(f"  {path.name}: {name} -> снят избыточный `type: object` при oneOf")

    # снизу вверх, чтобы номера строк выше точки правки не смещались
    for start, end, new in sorted(edits, key=lambda e: e[0], reverse=True):
        lines[start:end] = new
    path.write_text("\n".join(lines), encoding="utf-8")
    return len(fams), variants, report


def main():
    files = sorted(OUT_DIR.glob("*.yaml"))
    if not files:
        print(f"Нет *.yaml в {OUT_DIR.relative_to(ROOT)} — сначала выполните: npx tsp compile .")
        return 2
    total = 0
    for f in files:
        n, variants, report = process(f)
        total += n
        for line in report:
            print(line)
        print(f"{f.name}: {n} семейств расплющено, {variants} вариантов")
    print(f"Итого: {total}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
