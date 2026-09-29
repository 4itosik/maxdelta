package controlapi

import (
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"maxmock/internal/store"
	"maxmock/internal/wire"
)

// maxUploadBytes — предел размера загружаемого файла. Реальный Max допускает
// до 4 ГБ для типа `file`, но мок живёт в закрытом контуре и хранит blob-ы на
// диске рядом с базой; 256 МБ покрывают любой сценарий ручного тестирования.
const maxUploadBytes = 256 << 20

// uploadFromBotPlatform — эндпоинт, адрес которого мок выдаёт в ответе
// POST /uploads. Это эмуляция CDN Max: его формат в OpenAPI-контракт не
// входит и повторяет документацию MAX («Медиафайлы»):
//
//	image                → {"photos": {"<ключ>": {"token": "..."}}}  токен из словаря бот кладёт в payload.token
//	video | audio | file → {"token": "..."}                          (UploadedInfo)
func (a *API) uploadFromBotPlatform(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	att, err := a.core.Store().AttachmentByToken(token)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "mock.attachment.not_found",
			"неизвестный токен загрузки: сначала вызовите POST /uploads")
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	if att.Uploaded {
		writeErr(w, http.StatusBadRequest, "mock.bad_request", "по этому токену файл уже загружен")
		return
	}

	filename, mimeType, size, err := a.storeBlob(r, token)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "mock.bad_request", err.Error())
		return
	}
	if err := a.core.Store().CompleteAttachment(token, filename, mimeType, blobPath(a.blobDir(), token), size); err != nil {
		fail(w, err)
		return
	}

	if att.Type == wire.AttachmentImage {
		writeJSON(w, http.StatusOK, map[string]any{
			"photos": map[string]wire.PhotoToken{"photo": {Token: token}},
		})
		return
	}
	writeJSON(w, http.StatusOK, wire.UploadedInfo{Token: token})
}

// uploadFromUI принимает файл, выбранный тестировщиком в веб-чате, и сразу
// помечает его загруженным: у клиента мессенджера двухфазной загрузки нет.
func (a *API) uploadFromUI(w http.ResponseWriter, r *http.Request) {
	bot, ok := a.bot(w, r)
	if !ok {
		return
	}
	token := "att." + randomHex()
	filename, mimeType, size, err := a.storeBlob(r, token)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "mock.bad_request", err.Error())
		return
	}
	att, err := a.core.SaveUploadedFile(bot.ID, uploadTypeFor(mimeType), filename, mimeType,
		blobPath(a.blobDir(), token), size)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, att)
}

// serveFile отдаёт залитый файл по ссылке, которую мок кладёт во вложения.
func (a *API) serveFile(w http.ResponseWriter, r *http.Request) {
	att, err := a.core.Store().AttachmentByToken(r.PathValue("token"))
	if errors.Is(err, store.ErrNotFound) || (err == nil && !att.Uploaded) {
		writeErr(w, http.StatusNotFound, "mock.attachment.not_found", "файл не найден")
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	f, err := os.Open(att.Path)
	if err != nil {
		writeErr(w, http.StatusNotFound, "mock.attachment.not_found", "файл не найден на диске")
		return
	}
	defer f.Close()

	if att.Mime != "" {
		w.Header().Set("Content-Type", att.Mime)
	}
	if att.Filename != "" {
		w.Header().Set("Content-Disposition",
			mime.FormatMediaType("inline", map[string]string{"filename": att.Filename}))
	}
	http.ServeContent(w, r, att.Filename, timeOf(att.CreatedAt), f)
}

func (a *API) blobDir() string { return a.core.Config().BlobDir }

func blobPath(dir, token string) string { return filepath.Join(dir, token) }

// storeBlob сохраняет тело запроса в blob-каталог.
//
// Формат заливки в контракт не входит, а разные клиенты шлют по-разному:
// принимаем и multipart/form-data (любое файловое поле), и сырое тело —
// чтобы мок не отвергал корректный по сути запрос из-за формы.
func (a *API) storeBlob(r *http.Request, token string) (filename, mimeType string, size int64, err error) {
	dir := a.blobDir()
	if err = os.MkdirAll(dir, 0o755); err != nil {
		return "", "", 0, err
	}
	dst, err := os.Create(blobPath(dir, token))
	if err != nil {
		return "", "", 0, err
	}
	defer dst.Close()

	body := http.MaxBytesReader(nil, r.Body, maxUploadBytes)
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "multipart/form-data") {
		filename, mimeType, size, err = copyMultipart(r, dst)
	} else {
		mimeType = ct
		size, err = io.Copy(dst, body)
	}
	if err != nil {
		return "", "", 0, err
	}
	if size == 0 {
		return "", "", 0, errors.New("пустое тело запроса: файл не передан")
	}
	if filename == "" {
		filename = token
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return filename, mimeType, size, nil
}

// copyMultipart находит первое файловое поле и копирует его в dst.
func copyMultipart(r *http.Request, dst io.Writer) (filename, mimeType string, size int64, err error) {
	mr, err := r.MultipartReader()
	if err != nil {
		return "", "", 0, err
	}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return "", "", 0, errors.New("в multipart-запросе нет файлового поля")
		}
		if err != nil {
			return "", "", 0, err
		}
		if part.FileName() == "" {
			_ = part.Close()
			continue
		}
		n, err := io.Copy(dst, io.LimitReader(part, maxUploadBytes))
		_ = part.Close()
		if err != nil {
			return "", "", 0, err
		}
		return part.FileName(), partContentType(part), n, nil
	}
}

func partContentType(p *multipart.Part) string {
	if ct := p.Header.Get("Content-Type"); ct != "" {
		return ct
	}
	if ext := filepath.Ext(p.FileName()); ext != "" {
		if byExt := mime.TypeByExtension(ext); byExt != "" {
			return byExt
		}
	}
	return ""
}

// uploadTypeFor выбирает тип вложения Max по MIME-типу файла.
func uploadTypeFor(mimeType string) string {
	switch {
	case strings.HasPrefix(mimeType, "image/"):
		return wire.AttachmentImage
	case strings.HasPrefix(mimeType, "video/"):
		return wire.AttachmentVideo
	case strings.HasPrefix(mimeType, "audio/"):
		return wire.AttachmentAudio
	default:
		return wire.AttachmentFile
	}
}
