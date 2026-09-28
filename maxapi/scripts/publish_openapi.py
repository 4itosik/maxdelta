#!/usr/bin/env python3
"""Финальный шаг конвейера: публикация готовых OpenAPI-документов в корень maxapi/.

`tsp compile` кладёт результат в tsp-output/@typespec/openapi3/ — путь,
продиктованный эмиттером, а не нами: имя каталога собирается из имени пакета
эмиттера, и tspconfig.yaml задаёт лишь output-file внутри него. Каталог
промежуточный и целиком в .gitignore, потому что до конца конвейера лежащий
там YAML ещё не является контрактом: девять постобработчиков подряд правят
его (запечатывание additionalProperties, границы int, вычистка недостижимых
схем, разворачивание дискриминированных union'ов, снятие type: object у
nullable-обёрток, аннотации композитов).

Копируем готовое в maxapi/ — туда, где контракт видно без знания внутренностей
эмиттера и где он попадает в git. Именно эти два файла читают потребители:
scripts/openapi_to_jsonschema.py (генерация Go-структур) и подпроекты
maxbotdemo/maxmoc.

Копия, а не переезд output-dir: все прочие scripts/*.py работают по
tsp-output/, и смена пути затронула бы весь конвейер ради косметики. Копируем
байты, а не yaml.safe_load/dump — в начале каждого документа лежит комментарий
про оставшиеся oneOf/allOf, а round-trip через yaml его молча съест.
"""
import shutil
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
OUT_DIR = ROOT / "tsp-output" / "@typespec" / "openapi3"

DOCUMENTS = ("openapi.MaxBotApi.yaml", "openapi.MaxBotWebhook.yaml")


def main():
    missing = [name for name in DOCUMENTS if not (OUT_DIR / name).is_file()]
    if missing:
        print(
            f"Нет {', '.join(missing)} в {OUT_DIR.relative_to(ROOT)} — "
            f"сначала выполните: npx tsp compile ."
        )
        sys.exit(2)

    for name in DOCUMENTS:
        src = OUT_DIR / name
        dst = ROOT / name
        shutil.copyfile(src, dst)
        print(f"{name}: опубликован в {dst.relative_to(ROOT)} ({src.stat().st_size} Б)")

    print(f"Итого: {len(DOCUMENTS)} документа")


if __name__ == "__main__":
    main()
