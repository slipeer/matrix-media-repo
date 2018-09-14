package webserver

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/sirupsen/logrus"
	"github.com/turt2live/matrix-media-repo/api"
	"github.com/turt2live/matrix-media-repo/api/r0"
	"github.com/turt2live/matrix-media-repo/common"
	"github.com/turt2live/matrix-media-repo/util"
)

type handler struct {
	h          func(r *http.Request, entry *logrus.Entry) interface{}
	reqCounter *requestCounter
}

var loopback = net.IPNet{IP: net.IPv4(127, 0, 0, 0), Mask: net.IPv4Mask(255, 0, 0, 0)}

func XffParse(xff string) string {
    for _, ip := range strings.Split(xff, ",") {
	ip = strings.TrimSpace(ip)
	if IP := net.ParseIP(ip); IP != nil && !loopback.Contains(IP) {
	    return ip
	}
    }
    return ""
}

func (h handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	isUsingForwardedHost := false
	if r.Header.Get("X-Forwarded-Host") != "" {
		r.Host = r.Header.Get("X-Forwarded-Host")
		isUsingForwardedHost = true
	}
	r.Host = strings.Split(r.Host, ":")[0]

	if r.Header.Get("X-Forwarded-For") != "" {
		raddr := XffParse(r.Header.Get("X-Forwarded-For"))
		if raddr != "" {
			r.RemoteAddr = raddr
		}
	}

	contextLog := logrus.WithFields(logrus.Fields{
		"method":             r.Method,
		"host":               r.Host,
		"usingForwardedHost": isUsingForwardedHost,
		"resource":           r.URL.Path,
		"contentType":        r.Header.Get("Content-Type"),
		"contentLength":      r.ContentLength,
		"queryString":        util.GetLogSafeQueryString(r),
		"requestId":          h.reqCounter.GetNextId(),
		"remoteAddr":         r.RemoteAddr,
	})
	contextLog.Info("Received request")

	// Send CORS and other basic headers
	w.Header().Set("Access-Control-Allow-Headers", "Origin, X-Requested-With, Content-Type, Accept, Authorization")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'none'; plugin-types application/pdf; style-src 'unsafe-inline'; object-src 'self';")
	w.Header().Set("Cache-Control", "public,max-age=86400,s-maxage=86400")
	w.Header().Set("Server", "matrix-media-repo")

	// Process response
	var res interface{} = api.AuthFailed()
	if util.IsServerOurs(r.Host) {
		contextLog.Info("Server is owned by us, processing request")
		res = h.h(r, contextLog)
		if res == nil {
			res = &api.EmptyResponse{}
		}
	}
	if res == nil {
		res = api.InternalServerError("Error processing response")
	}

	contextLog.Info(fmt.Sprintf("Replying with result: %T %+v", res, res))

	statusCode := http.StatusOK
	switch result := res.(type) {
	case *api.ErrorResponse:
		switch result.InternalCode {
		case common.ErrCodeUnknownToken:
			statusCode = http.StatusUnauthorized
			break
		case common.ErrCodeNotFound:
			statusCode = http.StatusNotFound
			break
		case common.ErrCodeMediaTooLarge:
			statusCode = http.StatusRequestEntityTooLarge
			break
		case common.ErrCodeBadRequest:
			statusCode = http.StatusBadRequest
			break
		case common.ErrCodeMethodNotAllowed:
			statusCode = http.StatusMethodNotAllowed
			break
		default: // Treat as unknown (a generic server error)
			statusCode = http.StatusInternalServerError
			break
		}
		break
	case *r0.DownloadMediaResponse:
		w.Header().Set("Content-Type", result.ContentType)
		w.Header().Set("Content-Disposition", "inline; filename=\""+result.Filename+"\"")
		w.Header().Set("Content-Length", fmt.Sprint(result.SizeBytes))
		defer result.Data.Close()
		io.Copy(w, result.Data)
		return // Prevent sending conflicting responses
	case *r0.IdenticonResponse:
		w.Header().Set("Content-Type", "image/png")
		io.Copy(w, result.Avatar)
		return // Prevent sending conflicting responses
	default:
		break
	}

	// Order is important: Set headers before sending responses
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	encoder := json.NewEncoder(w)
	encoder.Encode(res)
}
