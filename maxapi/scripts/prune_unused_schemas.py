#!/usr/bin/env python3
"""Постобработка сгенерированного OpenAPI: удаление недостижимых схем.

@typespec/openapi3 эмиттит КАЖДУЮ модель, объявленную в namespace сервиса,
независимо от того, ссылается ли на неё хоть одна операция. Требование
SCHEMA-валидатора обратное: `Potentially unused component has been detected`
(см. review.md). Расхождение возникает в двух случаях:

  1. Модели, которые обслуживают выключенный набор маршрутов
     (`// import "./routes/chats.tsp"` в main.tsp) — ChatList, ChatPatch,
     ChatMembersList, ChatAdminsList, UserIdsList, ModifyMembersResult,
     ActionRequestBody, PinMessageBody, GetPinnedMessageResult.
  2. Сироты, унаследованные от оригинальной схемы MAX, где на них тоже
     никто не ссылается — BotPatch, PhotoTokens.
  3. Модели, попадающие в webhook-документ «за компанию» с namespace,
     но не используемые ни одним его событием — BotInfo, ChatMember.

Удаляем их из готового YAML, а не из *.tsp: исходники остаются полной
транскрипцией оригинала, и как только соответствующие маршруты включат
обратно, схемы автоматически снова окажутся достижимыми и сохранятся.

Достижимость считается транзитивно от всего документа за вычетом
components.schemas (paths, webhooks, components.parameters/responses/...);
ссылкой считается любая строка вида `#/components/schemas/<Имя>` — это
покрывает и `$ref`, и значения `discriminator.mapping`.

Побочный долг вычистки — осиротевшие базы. Эмиттер (seal-object-schemas)
не ставит `additionalProperties: false` схеме, от которой кто-то наследует:
в `allOf` каждая ветвь проверяет объект целиком, и флаг у базы отверг бы
собственные поля наследника. Но если единственный наследник вырезан здесь
как недостижимый (ModifyMembersResult у SimpleQueryResult), база перестаёт
быть базой, а флага у неё всё равно нет — требование КБ [59] нарушено без
причины. Такие схемы запечатываются тут же, сразу после вычистки.
"""
import re
import sys
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parent.parent
OUT_DIR = ROOT / "tsp-output" / "@typespec" / "openapi3"

REF = re.compile(r"#/components/schemas/([A-Za-z0-9_.\-]+)")


def collect_refs(node, out):
    """Все имена схем, упомянутые где-либо внутри поддерева."""
    if isinstance(node, dict):
        for k, v in node.items():
            collect_refs(k, out)
            collect_refs(v, out)
    elif isinstance(node, list):
        for v in node:
            collect_refs(v, out)
    elif isinstance(node, str):
        out.update(REF.findall(node))


def unreachable(doc):
    schemas = doc.get("components", {}).get("schemas", {})
    if not schemas:
        return set()
    # Корни: документ без самих схем.
    roots = {k: v for k, v in doc.items() if k != "components"}
    roots["components"] = {k: v for k, v in doc["components"].items() if k != "schemas"}
    seed = set()
    collect_refs(roots, seed)
    if not seed:
        print("Ни одной ссылки на схемы вне components.schemas — прерываю, чтобы не снести всё")
        sys.exit(2)
    reachable, queue = set(), [n for n in seed if n in schemas]
    while queue:
        name = queue.pop()
        if name in reachable:
            continue
        reachable.add(name)
        refs = set()
        collect_refs(schemas[name], refs)
        queue.extend(n for n in refs if n in schemas and n not in reachable)
    return set(schemas) - reachable


def inheritance_bases(schemas):
    """{база: {наследники}} по allOf-наследованию (у наследника свои properties)."""
    out = {}
    for name, schema in schemas.items():
        if not isinstance(schema, dict) or "properties" not in schema:
            continue
        seq = schema.get("allOf")
        if isinstance(seq, list) and seq and isinstance(seq[0], dict) and "$ref" in seq[0]:
            out.setdefault(seq[0]["$ref"].rsplit("/", 1)[-1], set()).add(name)
    return out


def orphaned_bases(doc, dead):
    """Базы, у которых все наследники попали под вычистку, а флага нет."""
    schemas = doc.get("components", {}).get("schemas", {})
    out = {}
    for base, heirs in inheritance_bases(schemas).items():
        schema = schemas.get(base)
        if base in dead or not isinstance(schema, dict) or not heirs <= dead:
            continue
        # Схема, которая сама наследует через allOf, стабов унаследованных
        # свойств не имеет (эмиттер выписывает их только не-базам) — флаг
        # отверг бы поля её базы. Такую сироту оставляем annotate_composites.
        if "allOf" in schema:
            continue
        if schema.get("type") == "object" and "additionalProperties" not in schema:
            out[base] = heirs
    return out


def seal_orphans(path, orphans):
    """Вставка `additionalProperties: false` сразу после блока properties."""
    lines = path.read_text(encoding="utf-8").splitlines(keepends=True)
    root = yaml.compose("".join(lines))
    components = next(v for k, v in root.value if k.value == "components")
    schemas = next(v for k, v in components.value if k.value == "schemas")
    inserts = []
    for name_node, schema_node in schemas.value:
        if name_node.value not in orphans:
            continue
        props_key, props = next((k, v) for k, v in schema_node.value if k.value == "properties")
        inserts.append((props.end_mark.line, props_key.start_mark.column))
    for line_no, column in sorted(inserts, reverse=True):
        lines.insert(line_no, f"{' ' * column}additionalProperties: false\n")
    path.write_text("".join(lines), encoding="utf-8")


def schema_line_spans(path):
    """{имя схемы: (первая строка, строка за последней)} — 0-индексно."""
    root = yaml.compose(path.read_text(encoding="utf-8"))
    components = next((v for k, v in root.value if k.value == "components"), None)
    if components is None:
        return {}
    schemas = next((v for k, v in components.value if k.value == "schemas"), None)
    if schemas is None:
        return {}
    starts = [(k.value, k.start_mark.line) for k, v in schemas.value]
    bounds = [ln for _, ln in starts[1:]] + [schemas.end_mark.line]
    return {name: (ln, end) for (name, ln), end in zip(starts, bounds)}


def main():
    files = sorted(OUT_DIR.glob("*.yaml"))
    if not files:
        print(f"Нет *.yaml в {OUT_DIR.relative_to(ROOT)} — сначала выполните: npx tsp compile .")
        sys.exit(2)
    total = 0
    for f in files:
        doc = yaml.safe_load(f.read_text(encoding="utf-8"))
        dead = unreachable(doc)
        if not dead:
            print(f"{f.name}: 0 недостижимых схем")
            continue
        spans = schema_line_spans(f)
        drop = set()
        for name in dead:
            start, end = spans[name]
            drop.update(range(start, end))
        lines = f.read_text(encoding="utf-8").splitlines(keepends=True)
        f.write_text("".join(l for i, l in enumerate(lines) if i not in drop), encoding="utf-8")
        total += len(dead)
        print(f"{f.name}: удалено {len(dead)} недостижимых схем: {', '.join(sorted(dead))}")
        orphans = orphaned_bases(doc, dead)
        if orphans:
            seal_orphans(f, orphans)
            for base, heirs in sorted(orphans.items()):
                print(
                    f"  {base}: запечатана (additionalProperties: false) — "
                    f"наследник {', '.join(sorted(heirs))} вырезан, база осиротела"
                )
    print(f"Итого: {total}")


if __name__ == "__main__":
    main()
