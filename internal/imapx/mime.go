package imapx

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"html"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"regexp"
	"strings"
	"time"
)

// parseInternalDate parses the IMAP INTERNALDATE format,
// for example "16-Nov-2026 09:30:00 +0300".
func parseInternalDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	layouts := []string{
		"2-Jan-2006 15:04:05 -0700",
		"_2-Jan-2006 15:04:05 -0700",
		"02-Jan-2006 15:04:05 -0700",
	}
	var lastErr error
	for _, layout := range layouts {
		t, err := time.Parse(layout, s)
		if err == nil {
			return t, nil
		}
		lastErr = err
	}
	return time.Time{}, lastErr
}

// parseMailDate parses an RFC 5322 date header as found inside an ENVELOPE.
func parseMailDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("imapx: пустая дата")
	}
	if t, err := mail.ParseDate(s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("imapx: не удалось разобрать дату %q", s)
}

const (
	maxBodyBytes  = 256 << 10 // extracted text is truncated at 256 KiB
	maxPartDepth  = 8
	maxParts      = 64
	maxTextAttach = 512 << 10 // single part is read up to 512 KiB
)

var htmlTagRe = regexp.MustCompile(`(?s)<(script|style)[^>]*>.*?</(script|style)>|<[^>]*>`)

// TextBody extracts a readable plain text representation of a raw RFC 822
// message. HTML alternatives are converted to text on a best effort basis.
func TextBody(raw []byte) string {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return ""
	}
	var sb strings.Builder
	walkParts(textproto.MIMEHeader(msg.Header), msg.Body, 0, &sb)
	return strings.TrimSpace(sb.String())
}

// SubjectFromRaw returns the decoded Subject header of a raw message.
func SubjectFromRaw(raw []byte) string {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return ""
	}
	return DecodeHeader(msg.Header.Get("Subject"))
}

func walkParts(h textproto.MIMEHeader, r io.Reader, depth int, sb *strings.Builder) {
	if depth > maxPartDepth || sb.Len() > maxBodyBytes {
		return
	}
	ctype := h.Get("Content-Type")
	mediaType := "text/plain"
	params := map[string]string{}
	if ctype != "" {
		mt, p, err := mime.ParseMediaType(ctype)
		if err == nil {
			mediaType = strings.ToLower(mt)
			params = p
		}
	}
	if strings.HasPrefix(mediaType, "multipart/") {
		boundary := params["boundary"]
		if boundary == "" {
			return
		}
		mr := multipart.NewReader(r, boundary)
		for i := 0; i < maxParts; i++ {
			part, err := mr.NextPart()
			if err != nil {
				return
			}
			walkParts(part.Header, part, depth+1, sb)
			if sb.Len() > maxBodyBytes {
				return
			}
		}
		return
	}
	if mediaType != "text/plain" && mediaType != "text/html" {
		return
	}
	body := decodeTransferEncoding(h.Get("Content-Transfer-Encoding"), r)
	text := Decode(params["charset"], body)
	if mediaType == "text/html" {
		text = normalizeHTML(text)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if sb.Len() > 0 {
		sb.WriteString("\n")
	}
	sb.WriteString(text)
}

func decodeTransferEncoding(encoding string, r io.Reader) []byte {
	enc := strings.ToLower(strings.TrimSpace(encoding))
	var reader io.Reader = r
	switch enc {
	case "base64":
		reader = base64.NewDecoder(base64.StdEncoding, &whitespaceStripper{r: r})
	case "quoted-printable":
		reader = quotedprintable.NewReader(r)
	}
	data, err := io.ReadAll(io.LimitReader(reader, maxTextAttach))
	if err != nil && len(data) == 0 {
		return nil
	}
	return data
}

// whitespaceStripper removes CR/LF so that base64 bodies split over lines
// decode correctly even when they contain unexpected padding.
type whitespaceStripper struct {
	r   io.Reader
	buf []byte
}

func (w *whitespaceStripper) Read(p []byte) (int, error) {
	if len(w.buf) == 0 {
		tmp := make([]byte, 4096)
		n, err := w.r.Read(tmp)
		if n == 0 {
			return 0, err
		}
		for _, c := range tmp[:n] {
			if c == '\r' || c == '\n' || c == ' ' || c == '\t' {
				continue
			}
			w.buf = append(w.buf, c)
		}
		if err != nil && len(w.buf) == 0 {
			return 0, err
		}
	}
	n := copy(p, w.buf)
	w.buf = w.buf[n:]
	return n, nil
}

func normalizeHTML(s string) string {
	s = htmlTagRe.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	return strings.Join(strings.Fields(s), " ")
}
