// Package specs загружает оба OpenAPI-контракта и даёт валидацию против них:
// входящих запросов Bot API, собственных ответов и исходящих webhook-тел.
package specs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/dlclark/regexp2"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"

	"maxmock/api"
)

// ErrRouteNotFound — путь/метод отсутствует в контракте Bot API.
var ErrRouteNotFound = errors.New("маршрут не описан в контракте")

// strictUpdateSchema — имя строгой ветви контракта вебхука: oneOf из 16
// конкретных типов событий с дискриминатором по update_type.
const strictUpdateSchema = "WebhookUpdate"

// Specs — загруженные контракты и построенные по ним роутеры.
type Specs struct {
	BotAPI  *openapi3.T
	Webhook *openapi3.T

	botRouter     routers.Router
	webhookRouter routers.Router
	webhookURL    string
	webhookMethod string
}

// noAuth отключает проверку security-требований: авторизацию мок выполняет
// сам, по измеренному поведению прода (голый токен в заголовке Authorization,
// см. maxfacade), а kin-openapi без этой функции считает любой запрос с
// описанным security неавторизованным.
func noAuth(context.Context, *openapi3filter.AuthenticationInput) error { return nil }

// ecmaMatcher адаптирует regexp2 под интерфейс kin-openapi.
type ecmaMatcher struct{ re *regexp2.Regexp }

func (m ecmaMatcher) MatchString(s string) bool {
	ok, err := m.re.MatchString(s)
	return err == nil && ok
}

// compileRegex компилирует `pattern` из спека движком с поддержкой
// ECMAScript-синтаксиса. Стандартный regexp Go построен на RE2 и не понимает
// опережающие проверки, а контракт их использует: `callback_id` объявлен как
// `^(?!\s*$).+` («не пробельная строка»).
func compileRegex(expr string) (openapi3.RegexMatcher, error) {
	re, err := regexp2.Compile(expr, regexp2.ECMAScript)
	if err != nil {
		return nil, err
	}
	return ecmaMatcher{re: re}, nil
}

func requestOptions() *openapi3filter.Options {
	return &openapi3filter.Options{
		AuthenticationFunc: noAuth,
		RegexCompiler:      compileRegex,
	}
}

func loadDoc(name string, adjust ...func(*openapi3.T) error) (*openapi3.T, error) {
	b, err := api.FS.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("чтение %s: %w", name, err)
	}
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(b)
	if err != nil {
		return nil, fmt.Errorf("разбор %s: %w", name, err)
	}
	normalizeAuthSchemes(doc)
	// Правки наносятся до Validate: документ должен пройти проверку ровно в
	// том виде, в котором по нему потом валидируются запросы.
	for _, fn := range adjust {
		if err := fn(doc); err != nil {
			return nil, fmt.Errorf("правка %s: %w", name, err)
		}
	}
	// Примеры в спеке снабжены ремарками и не обязаны проходить валидацию схем.
	if err := doc.Validate(loader.Context,
		openapi3.DisableExamplesValidation(),
		openapi3.SetRegexCompiler(compileRegex),
	); err != nil {
		return nil, fmt.Errorf("валидация документа %s: %w", name, err)
	}
	return doc, nil
}

// normalizeAuthSchemes приводит имя http-схемы авторизации к нижнему регистру.
// Артефакт объявляет `BearerAuth.scheme: Bearer`, тогда как реестр HTTP
// Authentication Schemes (RFC 7235) содержит имена в нижнем регистре и
// валидатор требует именно их. На поведение мока это не влияет: схему
// `BearerAuth` он не принимает вовсе (см. maxfacade), а правка нужна лишь
// затем, чтобы документ прошёл собственную валидацию.
func normalizeAuthSchemes(doc *openapi3.T) {
	if doc.Components == nil {
		return
	}
	for _, ref := range doc.Components.SecuritySchemes {
		if ref != nil && ref.Value != nil && ref.Value.Type == "http" {
			ref.Value.Scheme = strings.ToLower(ref.Value.Scheme)
		}
	}
}

// Адрес вебхука в контракте: `https://` и ничего больше.
const (
	httpsWebhookURL = `^https://.+$`
	anyWebhookURL   = `^https?://.+$`
	// anyStringURL — шаблон `url` в DELETE /subscriptions: «любая строка»,
	// потому что query-значение приходит percent-encoded (`https%3A%2F%2F…`),
	// и валидатор периметра сверял его с `^https://` до декодирования.
	anyStringURL = `^[\w\W]{1,2048}$`
)

// allowHTTPWebhookURL разрешает подписку на `http://`-адрес.
//
// Это не починка артефакта, а сознательное отступление мока от контракта —
// такое же, как паритет авторизации: контракт здесь прав (живой Max требует
// HTTPS, и `pattern` лишь переносит это требование из описания поля в
// проверяемую форму), но мок ставят в закрытый контур, где стенды КЦ живут
// на открытом HTTP, а выпустить им сертификат зачастую негде и некому.
// Отвергая такую подписку, мок становится в контуре неприменим — а он ровно
// для контура и сделан (docs/specs/2026-08-05-max-mock-design.md).
//
// Послабление минимальное: схема становится необязательной (`https?`), но
// проверка формы остаётся — строка без схемы или с чужой (`ftp://`, «адрес
// стенда») отвергается по-прежнему. Принятую http-подписку мок помечает в
// поле `message` ответа, чтобы отступление было видно вызывающему, а не
// только здесь (см. maxfacade.subscribe).
//
// Правится тело POST /subscriptions. Query-параметр DELETE /subscriptions
// контракт уже объявляет «любой строкой», так что снять http-подписку можно
// без правки — здесь лишь страж: ужесточи контракт шаблон обратно, и принятую
// подписку стало бы нельзя снять, поэтому загрузка падает.
func allowHTTPWebhookURL(doc *openapi3.T) error {
	body := doc.Components.Schemas["SubscriptionRequestBody"]
	if body == nil || body.Value == nil {
		return errors.New("в контракте нет схемы SubscriptionRequestBody")
	}
	if err := relaxURLScheme("SubscriptionRequestBody.url", body.Value.Properties["url"]); err != nil {
		return err
	}

	item := doc.Paths.Find("/subscriptions")
	if item == nil || item.Delete == nil {
		return errors.New("в контракте нет операции DELETE /subscriptions")
	}
	for _, p := range item.Delete.Parameters {
		if p.Value != nil && p.Value.Name == "url" {
			return requireAnyStringURL("DELETE /subscriptions?url", p.Value.Schema)
		}
	}
	return errors.New("у DELETE /subscriptions нет параметра url")
}

// relaxURLScheme заменяет паттерн одного поля-адреса.
//
// Несовпадение исходного паттерна — ошибка запуска, а не повод молча ничего
// не сделать: контракт переписали, и отступление нужно перепроверить, а не
// обнаружить однажды по отвергнутой подписке. Именно так это послабление и
// пропало в 0.0.33 — вместе с паттерном в контракте появилась ветка кода,
// до которой перестал доходить запрос.
func relaxURLScheme(where string, ref *openapi3.SchemaRef) error {
	if ref == nil || ref.Value == nil {
		return fmt.Errorf("в контракте нет поля %s", where)
	}
	if ref.Value.Pattern != httpsWebhookURL {
		return fmt.Errorf("%s: ожидался pattern %s, в контракте %q — послабление для http:// нужно перепроверить",
			where, httpsWebhookURL, ref.Value.Pattern)
	}
	ref.Value.Pattern = anyWebhookURL
	return nil
}

// requireAnyStringURL проверяет, что поле-адрес принимает любую строку, и
// ничего не правит. Другой шаблон — ошибка запуска по той же причине, что и в
// relaxURLScheme: отступление нужно перепроверить, а не обнаружить однажды по
// неснимаемой подписке.
func requireAnyStringURL(where string, ref *openapi3.SchemaRef) error {
	if ref == nil || ref.Value == nil {
		return fmt.Errorf("в контракте нет поля %s", where)
	}
	if ref.Value.Pattern != anyStringURL {
		return fmt.Errorf("%s: ожидался pattern %s, в контракте %q — снять http-подписку станет нельзя, послабление нужно перепроверить",
			where, anyStringURL, ref.Value.Pattern)
	}
	return nil
}

// Load читает оба контракта из embed.FS и строит роутеры.
func Load() (*Specs, error) {
	bot, err := loadDoc(api.BotAPIFile, allowHTTPWebhookURL)
	if err != nil {
		return nil, err
	}
	wh, err := loadDoc(api.WebhookFile)
	if err != nil {
		return nil, err
	}

	// Мок слушает на собственном адресе, а не на platform-api2.max.ru: если
	// оставить servers, роутер начнёт сверять хост запроса с хостом из спека
	// и не найдёт ни одного маршрута.
	bot.Servers = nil
	wh.Servers = nil

	botRouter, err := gorillamux.NewRouter(bot)
	if err != nil {
		return nil, fmt.Errorf("роутер Bot API: %w", err)
	}
	whRouter, err := gorillamux.NewRouter(wh)
	if err != nil {
		return nil, fmt.Errorf("роутер webhook: %w", err)
	}

	s := &Specs{BotAPI: bot, Webhook: wh, botRouter: botRouter, webhookRouter: whRouter}

	// В webhook-контракте ровно один путь и один метод — находим их один раз.
	for path, item := range wh.Paths.Map() {
		for method := range item.Operations() {
			s.webhookURL, s.webhookMethod = webhookSampleURL(path), method
		}
	}
	if s.webhookMethod == "" {
		return nil, errors.New("в webhook-контракте не найдено ни одной операции")
	}
	// Синтетический URL обязан сам проходить контракт: иначе каждое событие
	// падало бы на параметрах пути, не дойдя до проверки тела.
	if err := s.checkWebhookURL(); err != nil {
		return nil, err
	}
	return s, nil
}

// pathParam — шаблон параметра в пути контракта: `{integrationId}`.
var pathParam = regexp.MustCompile(`\{[^/}]+\}`)

// webhookPathParamSample — чем заполняются параметры пути в синтетическом
// URL (см. ValidateWebhookBody).
//
// Адресом мок не пользуется: события уходят на URL из подписки, а этот нужен
// лишь затем, чтобы kin-openapi нашёл операцию и проверил ТЕЛО. Но
// ValidateRequest заодно проверяет параметры пути, поэтому подставленное
// значение обязано удовлетворять их схемам. Нулевой UUID подходит под
// `integrationId` — единственный параметр нынешнего контракта; если формат
// параметра изменится, Load() скажет об этом сразу (checkWebhookURL), а не
// первым отвергнутым событием.
const webhookPathParamSample = "00000000-0000-0000-0000-000000000000"

func webhookSampleURL(path string) string {
	return "http://webhook.local" + pathParam.ReplaceAllString(path, webhookPathParamSample)
}

// checkWebhookURL сверяет синтетический URL с контрактом — всё, кроме тела:
// маршрут находится, параметры пути схемам соответствуют.
func (s *Specs) checkWebhookURL() error {
	req, err := http.NewRequest(s.webhookMethod, s.webhookURL, nil)
	if err != nil {
		return err
	}
	route, params, err := s.webhookRouter.FindRoute(req)
	if err != nil {
		return fmt.Errorf("маршрут webhook не найден по %s: %w", s.webhookURL, err)
	}
	opts := requestOptions()
	opts.ExcludeRequestBody = true
	if err := openapi3filter.ValidateRequest(context.Background(), &openapi3filter.RequestValidationInput{
		Request:     req,
		PathParams:  params,
		QueryParams: url.Values{},
		Route:       route,
		Options:     opts,
	}); err != nil {
		return fmt.Errorf("подставленное значение параметров пути (%s) не проходит webhook-контракт: %w",
			webhookPathParamSample, err)
	}
	return nil
}

// Version возвращает версию контракта Bot API.
func (s *Specs) Version() string { return s.BotAPI.Info.Version }

// FindRoute ищет операцию контракта, соответствующую запросу.
// Возвращает ErrRouteNotFound, если такой операции нет.
func (s *Specs) FindRoute(r *http.Request) (*routers.Route, map[string]string, error) {
	route, params, err := s.botRouter.FindRoute(r)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %s %s", ErrRouteNotFound, r.Method, r.URL.Path)
	}
	return route, params, nil
}

func requestInput(r *http.Request, route *routers.Route, params map[string]string) *openapi3filter.RequestValidationInput {
	return &openapi3filter.RequestValidationInput{
		Request:    r,
		PathParams: params,
		Route:      route,
		Options:    requestOptions(),
	}
}

// ValidateRequest проверяет параметры и тело запроса против контракта.
//
// После валидации r.Body содержит ровно те байты, что прислал клиент.
// Это не бесплатно: kin-openapi по ходу валидации подменяет тело собственной
// пересборкой разобранного JSON — с применёнными `default` и переупорядоченными
// ключами. Обработчику нужен оригинал: значения по умолчанию (`notify`,
// `disable_link_preview`) мок подставляет сам, а различие «поле пришло со
// значением null» и «поля не было» в пересборке теряется.
func (s *Specs) ValidateRequest(ctx context.Context, r *http.Request, route *routers.Route, params map[string]string) error {
	var raw []byte
	if r.Body != nil && r.Body != http.NoBody {
		var err error
		if raw, err = io.ReadAll(r.Body); err != nil {
			return fmt.Errorf("чтение тела запроса: %w", err)
		}
		_ = r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(raw))
		defer func() { r.Body = io.NopCloser(bytes.NewReader(raw)) }()
	}
	return openapi3filter.ValidateRequest(ctx, requestInput(r, route, params))
}

// ValidateResponse проверяет собственный ответ мока против контракта.
// Статусы, не описанные в спеке (например, 400 — его в контракте Max нет),
// пропускаются: их форму гарантирует схема Error на стороне мока.
func (s *Specs) ValidateResponse(ctx context.Context, r *http.Request, route *routers.Route, params map[string]string, status int, header http.Header, body []byte) error {
	input := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: requestInput(r, route, params),
		Status:                 status,
		Header:                 header,
		Body:                   io.NopCloser(bytes.NewReader(body)),
		Options:                requestOptions(),
	}
	return openapi3filter.ValidateResponse(ctx, input)
}

// ValidateWebhookBody проверяет тело исходящего события против контракта
// webhook-эндпоинта (openapi.MaxBotWebhook.yaml).
//
// Проверок две, и вторая обязательна. Тело операции описано как
// `anyOf: [UpdateUnified, WebhookUpdate]` — две равноправные формы на выбор
// РАЗРАБОТЧИКА БОТА: плоская схема, где обязательны только `update_type` и
// `timestamp`, и строгий oneOf из 16 конкретных типов. Для приёмника это
// удобство, но `anyOf` проходит, если совпала ХОТЬ ОДНА ветвь, — а значит
// плоская форма делает необязательными 37 полей, которые обязательны у
// конкретных событий. Через неё пролезает `message_created` без `message`,
// `user_added` без `chat_id`, событие с неизвестным `update_type` и даже
// смесь полей от разных вариантов.
//
// Мок обязан держать себя строже, чем контракт разрешает приёмнику: он не
// принимает событие, а порождает его, и снисходительность здесь означала бы
// молча отправленное на стенд событие, которого живой Max не присылает.
// Поэтому вдобавок к операции тело сверяется напрямую со строгой ветвью.
func (s *Specs) ValidateWebhookBody(ctx context.Context, body []byte) error {
	req, err := http.NewRequest(s.webhookMethod, s.webhookURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	route, params, err := s.webhookRouter.FindRoute(req)
	if err != nil {
		return fmt.Errorf("маршрут webhook не найден: %w", err)
	}
	if err := openapi3filter.ValidateRequest(ctx, &openapi3filter.RequestValidationInput{
		Request:     req,
		PathParams:  params,
		QueryParams: url.Values{},
		Route:       route,
		Options:     requestOptions(),
	}); err != nil {
		return err
	}
	return s.validateStrictUpdate(body)
}

// validateStrictUpdate сверяет событие со схемой WebhookUpdate — строгой
// ветвью anyOf (см. ValidateWebhookBody).
func (s *Specs) validateStrictUpdate(body []byte) error {
	ref := s.Webhook.Components.Schemas[strictUpdateSchema]
	if ref == nil || ref.Value == nil {
		// Схема пропала из контракта — это ошибка сборки, а не повод
		// незаметно ослабить проверку.
		return fmt.Errorf("схема %s отсутствует в контракте webhook", strictUpdateSchema)
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return fmt.Errorf("тело события не разобрано: %w", err)
	}
	if err := s.Webhook.ValidateSchemaJSON(ref.Value, v); err != nil {
		return fmt.Errorf("событие не соответствует %s: %w", strictUpdateSchema, err)
	}
	return nil
}
