.PHONY: all check-sync install install-tools build gen gen-quicktype gen-oapi-codegen compare-gen example-quicktype example-oapi-codegen examples test compare validate clean

# ЕДИНСТВЕННАЯ команда, которую нужно помнить: `make`.
#
# Пересобирает контракт из *.tsp, публикует его в корень, проверяет требования
# КБ, перегенерирует ОБА варианта Go-структур и прогоняет тесты — свои и обоих
# примеров.
#
# Всё разом, потому что в git лежат оба конца цепочки: openapi.MaxBotApi.yaml
# читает валидатор на периметре, models.go читает бот. Обновлять их порознь
# нельзя — разойдутся молча, и ни компилятор, ни тесты этого не увидят: по
# отдельности оба файла корректны.
#
# `compare` сюда не входит намеренно: сверка с официальной схемой сейчас
# красная из-за выключенных маршрутов /chats (см. `// import "./routes/chats.tsp"`
# в main.tsp), и держать команду на каждый день красной незачем. Она в `test`.
all: validate gen examples

# Та же перегенерация плюс вопрос к git: изменилось ли что-нибудь.
# Если да — в коммите лежали артефакты, не соответствующие *.tsp.
# Место этой цели — CI и pre-push, а не ежедневная работа.
check-sync: all
	python3 scripts/check_sync.py

node_modules: package.json
	npm install
	@touch node_modules

# Кодогенераторы с закреплёнными версиями. Ставятся обычным `go install`
# туда, куда их кладёт Go ($GOBIN, иначе $GOPATH/bin), и вызываются по
# имени. Версия задана один раз, в scripts/install_tools.py; скрипт
# идемпотентен, переставляет бинарь при разъезде версии и проверяет, что
# каталог установки виден в PATH.
install-tools:
	python3 scripts/install_tools.py

install: node_modules install-tools

build: node_modules
	npx tsp compile .
	python3 scripts/seal_additional_properties.py
	python3 scripts/fill_inherited_stubs.py
	python3 scripts/apply_int_bounds.py
	python3 scripts/prune_unused_schemas.py
	python3 scripts/flatten_discriminated_unions.py
	python3 scripts/strip_nullable_wrapper_type.py
	python3 scripts/annotate_composites.py
	python3 scripts/publish_openapi.py

gen-quicktype: build
	python3 scripts/openapi_to_jsonschema.py
	npx quicktype -s schema -l go --package maxapi --just-types-and-package --omit-empty --no-combine-classes -t MaxBotAPI -o gen/quicktype/models.go gen/max.schema.json
	python3 scripts/finalize_go_models.py
	cd gen/quicktype && go build ./... && go test ./...

# Второй вариант кодогенерации: oapi-codegen читает контракт напрямую.
# Версия закреплена — генератор должен давать тот же результат на том же
# контракте, иначе models.go начнёт «плавать» между машинами.
gen-oapi-codegen: build install-tools
	oapi-codegen -config gen/oapi-codegen/oapi-codegen.yaml openapi.MaxBotApi.yaml
	cd gen/oapi-codegen && go build ./... && go test ./...

# Оба варианта разом — их и сравнивает compare-gen.
gen: gen-quicktype gen-oapi-codegen

compare-gen: gen examples
	python3 scripts/compare_generators.py

example-quicktype: gen-quicktype
	cd example-quicktype && go build ./... && go vet ./... && go test ./...

example-oapi-codegen: gen-oapi-codegen
	cd example-oapi-codegen && go build ./... && go vet ./... && go test ./...

# Два примера, решающих одну задачу на двух вариантах кодогенерации.
# Их и сравнивает compare-gen: считает рукописный код построчно.
examples: example-quicktype example-oapi-codegen

compare: build
	python3 scripts/compare.py

validate: build
	python3 scripts/validate_kb.py -q

test: validate gen examples compare

clean:
	rm -rf tsp-output gen/max.schema.json gen/compare.json
