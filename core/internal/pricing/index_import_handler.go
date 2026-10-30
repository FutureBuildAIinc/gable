// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pricing

import (
	"io"
	"net/http"
	"time"

	"github.com/gablelbm/gable/pkg/httputil"
)

// The market index import and staleness routes (QE-07-b1).
//
// The import takes a CSV of index values, previews what it would do and
// applies it. The staleness route answers how old the active indices' values
// are, which is what the banner on the market indices screen, the quotes
// exposure screen and the quote page shows.

// importUploadLimit is the largest import file the server takes. A file of
// index values is small; a bigger one is refused rather than read.
const importUploadLimit = 4 << 20

// IndexImportHandler serves the market index import and staleness routes.
type IndexImportHandler struct {
	imports   *IndexImportService
	staleness *IndexStalenessService
}

// NewIndexImportHandler returns the import handler.
func NewIndexImportHandler(imports *IndexImportService, staleness *IndexStalenessService) *IndexImportHandler {
	return &IndexImportHandler{imports: imports, staleness: staleness}
}

// RegisterRoutes adds the import and staleness handlers to the mux.
func (h *IndexImportHandler) RegisterRoutes(mux *http.ServeMux, roleGuard ...func(http.Handler) http.Handler) {
	guard := func(handler http.HandlerFunc) http.HandlerFunc {
		if len(roleGuard) > 0 && roleGuard[0] != nil {
			return func(w http.ResponseWriter, r *http.Request) {
				roleGuard[0](handler).ServeHTTP(w, r)
			}
		}
		return handler
	}

	mux.HandleFunc("POST /api/v1/market-indices/import/preview", guard(h.HandleImportPreview))
	mux.HandleFunc("POST /api/v1/market-indices/import/apply", guard(h.HandleImportApply))
	mux.HandleFunc("GET /api/v1/market-indices/staleness", guard(h.HandleStaleness))
}

// HandleImportPreview reads the file and answers what it would do. It writes
// nothing.
func (h *IndexImportHandler) HandleImportPreview(w http.ResponseWriter, r *http.Request) {
	data, ferr := readImportFile(w, r)
	if ferr != nil {
		httputil.RespondError(w, r, ferr.message, ferr.status, ferr.cause)
		return
	}
	// A file the server cannot read is the caller's mistake; a read that
	// fails is the server's.
	if _, err := ParseIndexCSV(data); err != nil {
		httputil.RespondError(w, r, err.Error(), http.StatusBadRequest, err)
		return
	}
	preview, err := h.imports.Preview(r.Context(), data)
	if err != nil {
		httputil.RespondError(w, r, "failed to read the import file", http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

// HandleImportApply writes the file's rows. Every row it accepted in the
// preview is written here, and the exposure scan runs once for each index
// whose value moved.
func (h *IndexImportHandler) HandleImportApply(w http.ResponseWriter, r *http.Request) {
	data, ferr := readImportFile(w, r)
	if ferr != nil {
		httputil.RespondError(w, r, ferr.message, ferr.status, ferr.cause)
		return
	}
	if _, err := ParseIndexCSV(data); err != nil {
		httputil.RespondError(w, r, err.Error(), http.StatusBadRequest, err)
		return
	}
	result, err := h.imports.Apply(r.Context(), data, userIDString(r))
	if err != nil {
		httputil.RespondError(w, r, "failed to apply the import", http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// HandleStaleness answers how old the active indices' values are.
func (h *IndexImportHandler) HandleStaleness(w http.ResponseWriter, r *http.Request) {
	doc, err := h.staleness.Staleness(r.Context(), time.Now())
	if err != nil {
		httputil.RespondError(w, r, "failed to read the market index freshness", http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

// importFileError is a failure to read the uploaded file, with the answer
// the client gets.
type importFileError struct {
	message string
	status  int
	cause   error
}

func (e *importFileError) Error() string { return e.message }

// readImportFile reads the uploaded CSV. The file arrives as multipart form
// data in a "file" field.
func readImportFile(w http.ResponseWriter, r *http.Request) ([]byte, *importFileError) {
	if err := r.ParseMultipartForm(importUploadLimit); err != nil {
		return nil, &importFileError{message: "the file could not be read", status: http.StatusBadRequest, cause: err}
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		return nil, &importFileError{message: "a file is required", status: http.StatusBadRequest, cause: err}
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, importUploadLimit+1))
	if err != nil {
		return nil, &importFileError{message: "the file could not be read", status: http.StatusBadRequest, cause: err}
	}
	if len(data) > importUploadLimit {
		return nil, &importFileError{message: "the file is too large", status: http.StatusRequestEntityTooLarge}
	}
	if len(data) == 0 {
		return nil, &importFileError{message: "the file is empty", status: http.StatusBadRequest}
	}
	return data, nil
}
