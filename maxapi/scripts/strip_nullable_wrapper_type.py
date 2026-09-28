#!/usr/bin/env python3
"""Постобработка сгенерированного OpenAPI: снятие `type: object` у nullable-обёрток.

@typespec/openapi3 переводит `Model | null` в обёртку

    {type: object, allOf: [{$ref: Model}], nullable: true}

и ставит `type: object` из-за буквы OAS 3.0.3: nullable «действует только
рядом с явно указанным type». Но правило КБ [59] («у object-схемы явные
свойства и additionalProperties: false») срабатывает именно на `type: object`,
а поставить флаг на обёртку нельзя: additionalProperties не смотрит сквозь
allOf, и `false` отверг бы все поля целевой схемы — поле принимало бы только
`{}` и `null`.

Снимаем `type: object`. Это форма самого контракта MAX: из 19 таких обёрток
в оригинале 16 записаны без type (Chat.icon, Chat.pinned_message,
CallbackAnswer.message). Замерено, что потребители разницы не видят:
Go-модели oapi-codegen совпадают байт в байт, переходник quicktype обе формы
понимает, kin-openapi (на нём maxmoc) принимает null в обеих. Цена, которую
стоит знать: AJV-валидаторы схему с nullable без type не компилируют, а
строгие по букве стандарта валидаторы null через такую обёртку не пропускают
ни с type, ни без — мешает ветвь allOf, которой null не подходит. Это свойство
формы оригинала, не наше.

Снимаем только у обёрток: allOf из одного $ref, nullable: true, без своих
properties. У схем с properties `type: object` остаётся. Правка построчная,
остальной файл не переформатируется; повторный прогон ничего не находит.

Место в конвейере — после расплющивания union'ов и до аннотации композитов:
annotate_composites.py должен видеть уже итоговую форму обёрток.
"""
import re
import sys
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parent.parent
OUT_DIR = ROOT / "tsp-output" / "@typespec" / "openapi3"

TYPE_LINE = re.compile(r"^\s*type: object\s*$")


def node_get(node, key):
    for k, v in node.value:
        if k.value == key:
            return v
    return None


def is_nullable_wrapper(node):
    """{type: object, allOf: [{$ref}], nullable: true} без своих properties."""
    keys = {k.value for k, _ in node.value}
    if not {"type", "allOf", "nullable"} <= keys or "properties" in keys:
        return False
    tnode, nnode, allof = node_get(node, "type"), node_get(node, "nullable"), node_get(node, "allOf")
    if not (isinstance(tnode, yaml.ScalarNode) and tnode.value == "object"):
        return False
    if not (isinstance(nnode, yaml.ScalarNode) and nnode.value == "true"):
        return False
    if not (isinstance(allof, yaml.SequenceNode) and len(allof.value) == 1):
        return False
    item = allof.value[0]
    return isinstance(item, yaml.MappingNode) and {k.value for k, _ in item.value} == {"$ref"}


def collect(node, out):
    """Строки ключей `type` у всех nullable-обёрток документа (0-индексно)."""
    if isinstance(node, yaml.SequenceNode):
        for item in node.value:
            collect(item, out)
        return
    if not isinstance(node, yaml.MappingNode):
        return
    if is_nullable_wrapper(node):
        key = next(k for k, _ in node.value if k.value == "type")
        out.append(key.start_mark.line)
    for _, value in node.value:
        collect(value, out)


def strip(path):
    lines = path.read_text(encoding="utf-8").splitlines(keepends=True)
    targets = []
    collect(yaml.compose("".join(lines)), targets)
    removed = 0
    for line_no in sorted(targets, reverse=True):
        if not TYPE_LINE.match(lines[line_no]):
            print(f"  {path.name}:{line_no + 1}: пропуск, ключ не на своей строке: {lines[line_no].strip()[:60]}")
            continue
        del lines[line_no]
        removed += 1
    if removed:
        path.write_text("".join(lines), encoding="utf-8")
    return removed


def main():
    files = sorted(OUT_DIR.glob("*.yaml"))
    if not files:
        print(f"Нет *.yaml в {OUT_DIR.relative_to(ROOT)} — сначала выполните: npx tsp compile .")
        sys.exit(2)
    total = 0
    for f in files:
        n = strip(f)
        total += n
        print(f"{f.name}: снят type: object у {n} nullable-обёрток")
    print(f"Итого: {total}")


if __name__ == "__main__":
    main()
