// Package base provide General functions of tupu content recognition interface
package base

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var (
	ErrBadGateway         = errors.New("502 Bad Gateway")
	ErrServiceUnavailable = errors.New("503 Service Unavailable")
)

const (
	ROOT_API_URL = "http://api.open.tuputech.com/v3/recognition/"
	//ROOT_API_URL = "http://172.26.2.63:8991/v3/recognition/"
	USER_AGENT = "tupu-client/1.0"
)

// Handler is a client-side helper to access TUPU recognition service
type Handler struct {
	apiURL   string
	signer   Signer
	verifier Verifier
	//
	UID       string //for sub-user statistics and billing
	UserAgent string
	Client    *http.Client
}

// NewHandler is an initializer for a Handler
func NewHandler(privateKeyPath string) (*Handler, error) {
	h := new(Handler)
	h.apiURL = ROOT_API_URL
	h.UserAgent = USER_AGENT
	h.Client = &http.Client{}

	var e error
	if h.verifier, e = LoadTupuPublicKey(); e != nil {
		return nil, e
	}
	if h.signer, e = LoadPrivateKey(privateKeyPath); e != nil {
		return nil, e
	}
	return h, nil
}

// NewHandlerWithURL is also an initializer for a Handler
func NewHandlerWithURL(privateKeyPath, url string) (h *Handler, e error) {
	if h, e = NewHandler(privateKeyPath); e != nil {
		return
	}
	h.apiURL = url
	return h, nil
}

// RecognizeWithURL is a shortcut for initiating a recognition request with URLs of dataInfoSlice
func (h *Handler) RecognizeWithURL(requestParam, secretID string, URLs []string, otherMsg map[string][]string) (result string, statusCode int, e error) {
	var dataInfoSlice []*DataInfo
	for _, val := range URLs {
		dataInfoSlice = append(dataInfoSlice, NewRemoteDataInfo(val))
	}
	return h.Recognize(requestParam, secretID, dataInfoSlice, otherMsg)
}

// RecognizeWithPath is a shortcut for initiating a recognition request with paths of dataInfoSlice
func (h *Handler) RecognizeWithPath(requestParam, secretID string, imagePaths []string, otherMsg map[string][]string) (result string, statusCode int, e error) {
	var dataInfoSlice []*DataInfo
	for _, val := range imagePaths {
		dataInfoSlice = append(dataInfoSlice, NewLocalDataInfo(val))
	}
	return h.Recognize(requestParam, secretID, dataInfoSlice, otherMsg)
}

// Recognize is the major method for initiating a recognition request
func (h *Handler) Recognize(requestParam, secretID string, dataInfoSlice []*DataInfo, otherMsg map[string][]string) (result string, statusCode int, e error) {
	t := time.Now()
	timestamp := strconv.FormatInt(t.Unix(), 10)
	r := rand.New(rand.NewSource(t.UnixNano()))
	nonce := strconv.FormatInt(int64(r.Uint32()), 10)
	forSign := strings.Join([]string{secretID, timestamp, nonce}, ",")
	var signature string
	if signature, e = h.sign([]byte(forSign)); e != nil {
		return
	}

	params := map[string]string{
		"timestamp": timestamp,
		"nonce":     nonce,
		"signature": signature,
	}
	if len(h.UID) > 0 {
		params["uid"] = h.UID
	}

	var (
		url  = h.apiURL + secretID
		req  *http.Request
		resp *http.Response
	)
	if req, e = h.request(requestParam, &url, &params, dataInfoSlice, otherMsg); e != nil {
		//log.Fatal(e)
		return
	}

	if resp, e = h.Client.Do(req); e != nil {
		//log.Fatal(e)
		return
	}
	if result, statusCode, e = h.processResp(resp); e != nil {
		//log.Fatal(e)
		return
	}
	//fmt.Println(resp.Header)
	return
}

func (h *Handler) sign(message []byte) (string, error) {
	signed, e := h.signer.Sign(message)
	if e != nil {
		return "", fmt.Errorf("could not sign message: %v", e)
	}
	return base64.StdEncoding.EncodeToString(signed), nil
}

func (h *Handler) verify(message []byte, sig string) error {
	data, e := base64.StdEncoding.DecodeString(sig)
	if e != nil {
		return fmt.Errorf("could not decode with Base64: %v", e)
	}

	e = h.verifier.Verify(message, data)
	if e != nil {
		return fmt.Errorf("could not verify request: %v", e)
	}
	return nil
}

func (h *Handler) request(requestParam string, url *string, params *map[string]string, dataInfoSlice []*DataInfo, otherMsg map[string][]string) (req *http.Request, e error) {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	for key, val := range *params {
		_ = writer.WriteField(key, val)
	}

	// write binary data to request body
	for index, dataInfoItem := range dataInfoSlice {
		if e = addDataInfoField(requestParam, writer, dataInfoItem, index); e == nil {
			// with other message
			if otherMsg != nil {
				for kStr, vSlice := range otherMsg {
					var vSliceLen int
					if vSlice != nil {
						vSliceLen = len(vSlice)
					}
					if vSliceLen > 0 && index < vSliceLen {
						_ = writer.WriteField(kStr, vSlice[index])
					}
				}
			}
		}
	}

	if e = writer.Close(); e != nil {
		return
	}

	if req, e = http.NewRequest("POST", *url, body); e != nil {
		return
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("User-Agent", h.UserAgent)
	req.Header.Set("Timeout", "30")

	// fmt.Println("########## debug #########")
	// fmt.Printf("*********** %v **************\n", req.Body)
	// //fmt.Println(writer)
	// fmt.Println("########## debug #########")

	// fmt.Println(req.Header)
	// fmt.Println(body.String())
	return
}

func addDataInfoField(requestParams string, writer *multipart.Writer, item *DataInfo, idx int) (e error) {
	switch {
	case len(item.url) > 0:
		_ = writer.WriteField(requestParams, item.url)
	case len(item.path) > 0:
		var (
			file *os.File
			part io.Writer
		)
		if file, e = os.Open(item.path); e != nil {
			return
		}
		part, e = writer.CreateFormFile(requestParams, filepath.Base(item.path))
		if e == nil {
			_, e = io.Copy(part, file)
		}
		file.Close()
	case item.buf != nil && item.buf.Len() > 0 && len(item.filename) > 0:
		var part io.Writer
		part, e = writer.CreateFormFile(requestParams, item.filename)
		if e == nil {
			_, e = io.Copy(part, item.buf)
		}
	default:
		return fmt.Errorf("invalid data resource at index [%v]", idx)
	}
	return
}

func (h *Handler) processResp(resp *http.Response) (result string, statusCode int, e error) {
	statusCode = resp.StatusCode
	//if resp.StatusCode > 500 {
	//	if resp.StatusCode == 502 {
	//		e = ErrBadGateway
	//	} else if resp.StatusCode == 503 {
	//		e = ErrServiceUnavailable
	//	} else {
	//		e = errors.New(resp.Status)
	//	}
	//	return
	//}

	body := &bytes.Buffer{}
	if _, e = body.ReadFrom(resp.Body); e != nil {
		return
	}
	if e = resp.Body.Close(); e != nil {
		return
	}

	var (
		data map[string]string
		ok   bool
		sig  string
	)
	if err := json.Unmarshal(body.Bytes(), &data); err != nil {
		if statusCode == 400 || statusCode <= 299 {
			e = errors.New("missing valid response body")
		}
		return
	} else if result, ok = data["json"]; !ok {
		e = errors.New("no result string")
		return
	} else if sig, ok = data["signature"]; !ok {
		e = errors.New("no server signature")
		return
	}
	e = h.verify([]byte(result), sig)
	return
}