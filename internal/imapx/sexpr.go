package imapx

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// errSyntax is returned when an IMAP response cannot be tokenised.
var errSyntax = errors.New("imapx: некорректный ответ сервера")

// sexpr is a node of an IMAP S-expression: either an atom, a quoted string or
// a parenthesised list.
type sexpr struct {
	atom   string
	quoted bool
	list   []*sexpr
	isList bool
	nilVal bool
}

// String returns the atom value of the node.
func (n *sexpr) String() string {
	if n == nil {
		return ""
	}
	return n.atom
}

// item returns the i-th child of a list node or nil.
func (n *sexpr) item(i int) *sexpr {
	if n == nil || !n.isList || i < 0 || i >= len(n.list) {
		return nil
	}
	return n.list[i]
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\n' }

// parseSexpr parses one S-expression starting at s[i] and returns the next
// unread offset.
func parseSexpr(s string, i int) (*sexpr, int, error) {
	for i < len(s) && isSpace(s[i]) {
		i++
	}
	if i >= len(s) {
		return nil, i, errSyntax
	}
	switch s[i] {
	case '(':
		i++
		node := &sexpr{isList: true}
		for {
			for i < len(s) && isSpace(s[i]) {
				i++
			}
			if i >= len(s) {
				return nil, i, errSyntax
			}
			if s[i] == ')' {
				return node, i + 1, nil
			}
			child, next, err := parseSexpr(s, i)
			if err != nil {
				return nil, i, err
			}
			node.list = append(node.list, child)
			i = next
		}
	case '"':
		i++
		var sb strings.Builder
		for i < len(s) {
			c := s[i]
			if c == '\\' && i+1 < len(s) {
				sb.WriteByte(s[i+1])
				i += 2
				continue
			}
			if c == '"' {
				return &sexpr{atom: sb.String(), quoted: true}, i + 1, nil
			}
			sb.WriteByte(c)
			i++
		}
		return nil, i, errSyntax
	default:
		start := i
		for i < len(s) && !isSpace(s[i]) && s[i] != ')' && s[i] != '(' {
			i++
		}
		atom := s[start:i]
		if strings.EqualFold(atom, "NIL") {
			return &sexpr{atom: "", nilVal: true}, i, nil
		}
		return &sexpr{atom: atom}, i, nil
	}
}

// parseFetch parses an untagged FETCH response line and extracts the UID,
// the message date, the sender addresses and the subject.
//
// Example input:
//
//   - 12 FETCH (UID 4711 INTERNALDATE "16-Nov-2026 09:30:00 +0300" ENVELOPE (...))
func parseFetch(line string) (header Header, ok bool, err error) {
	s := strings.TrimSpace(line)
	if !strings.HasPrefix(s, "* ") {
		return header, false, nil
	}
	s = s[2:]
	sp := strings.IndexByte(s, ' ')
	if sp < 0 {
		return header, false, nil
	}
	if _, convErr := strconv.Atoi(s[:sp]); convErr != nil {
		return header, false, nil
	}
	s = strings.TrimSpace(s[sp:])
	if len(s) < 5 || !strings.EqualFold(s[:5], "FETCH") {
		return header, false, nil
	}
	s = strings.TrimSpace(s[5:])
	if !strings.HasPrefix(s, "(") {
		return header, false, nil
	}
	node, _, err := parseSexpr(s, 0)
	if err != nil {
		return header, false, err
	}
	if !node.isList {
		return header, false, nil
	}
	var internalDate string
	for i := 0; i+1 < len(node.list); i += 2 {
		key := strings.ToUpper(node.list[i].atom)
		val := node.list[i+1]
		switch key {
		case "UID":
			uid, convErr := strconv.ParseUint(val.atom, 10, 32)
			if convErr != nil {
				return header, false, fmt.Errorf("imapx: некорректный UID %q", val.atom)
			}
			header.UID = uint32(uid)
		case "INTERNALDATE":
			internalDate = val.atom
		case "ENVELOPE":
			header.Subject = DecodeHeader(val.item(1).String())
			header.From = parseAddressList(val.item(2))
			if d, parseErr := parseMailDate(val.item(0).String()); parseErr == nil && !d.IsZero() {
				header.Date = d
			}
		}
	}
	if header.Date.IsZero() && internalDate != "" {
		if d, parseErr := parseInternalDate(internalDate); parseErr == nil {
			header.Date = d
		}
	}
	return header, true, nil
}

// parseAddressList converts an IMAP address list into readable "Name <addr>"
// strings.
func parseAddressList(n *sexpr) []string {
	if n == nil || !n.isList {
		return nil
	}
	var out []string
	for _, addr := range n.list {
		if addr == nil || !addr.isList || len(addr.list) < 4 {
			continue
		}
		name := DecodeHeader(addr.item(0).String())
		mailbox := addr.item(2).String()
		host := addr.item(3).String()
		if mailbox == "" || host == "" {
			// Group syntax or an incomplete address.
			if name != "" {
				out = append(out, name)
			}
			continue
		}
		mail := mailbox + "@" + host
		if name != "" && !strings.EqualFold(name, mail) {
			out = append(out, fmt.Sprintf("%s <%s>", name, mail))
		} else {
			out = append(out, mail)
		}
	}
	return out
}
