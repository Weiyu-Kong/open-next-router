package dslconfig

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/r9s-ai/open-next-router/onr-core/pkg/jsonutil"
)

func parseObservabilityBlock(s *scanner) (ProviderObservability, error) {
	var out ProviderObservability
	lb := s.nextNonTrivia()
	if lb.kind != tokLBrace {
		return out, s.errAt(lb, "expected '{' after observability")
	}
	for {
		tok := s.nextNonTrivia()
		switch tok.kind {
		case tokEOF:
			return ProviderObservability{}, s.errAt(tok, "unexpected EOF in observability block")
		case tokRBrace:
			return out, nil
		case tokIdent:
			switch tok.text {
			case "upstream_request_id":
				if out.UpstreamRequestID != nil {
					return ProviderObservability{}, s.errAt(tok, "duplicate upstream_request_id directive")
				}
				headers, err := parseUpstreamRequestIDHeaders(s)
				if err != nil {
					return ProviderObservability{}, err
				}
				out.UpstreamRequestID = &UpstreamRequestIDRule{Headers: headers}
			case "upstream_request_id_json":
				if out.UpstreamRequestIDJSON != nil {
					return ProviderObservability{}, s.errAt(tok, "duplicate upstream_request_id_json directive")
				}
				path, err := parseUpstreamRequestIDJSONPath(s)
				if err != nil {
					return ProviderObservability{}, err
				}
				out.UpstreamRequestIDJSON = &UpstreamRequestIDJSONRule{Path: path}
			default:
				return ProviderObservability{}, s.errAt(tok, fmt.Sprintf("unknown observability directive %q", tok.text))
			}
		default:
			return ProviderObservability{}, s.errAt(tok, "unexpected token in observability block")
		}
	}
}

func parseUpstreamRequestIDJSONPath(s *scanner) (string, error) {
	pathTok := s.nextNonTrivia()
	if pathTok.kind != tokString {
		return "", s.errAt(pathTok, "upstream_request_id_json expects one JSONPath string")
	}
	path := strings.TrimSpace(unquoteString(pathTok.text))
	if !jsonutil.ValidPath(path) {
		return "", s.errAt(pathTok, fmt.Sprintf("invalid upstream request ID JSONPath %q", path))
	}
	semi := s.nextNonTrivia()
	if semi.kind != tokSemicolon {
		return "", s.errAt(semi, "upstream_request_id_json expects one JSONPath string followed by ';'")
	}
	return path, nil
}

func parseUpstreamRequestIDHeaders(s *scanner) ([]string, error) {
	headers := make([]string, 0, 2)
	seen := map[string]struct{}{}
	for {
		tok := s.nextNonTrivia()
		switch tok.kind {
		case tokString:
			name := strings.TrimSpace(unquoteString(tok.text))
			if name == "" || !validHeaderFieldName(name) {
				return nil, s.errAt(tok, fmt.Sprintf("invalid upstream request ID header name %q", name))
			}
			key := strings.ToLower(name)
			if _, ok := seen[key]; ok {
				return nil, s.errAt(tok, fmt.Sprintf("duplicate upstream request ID header %q", name))
			}
			seen[key] = struct{}{}
			headers = append(headers, name)
		case tokSemicolon:
			if len(headers) == 0 {
				return nil, s.errAt(tok, "upstream_request_id requires at least one header name")
			}
			return headers, nil
		case tokEOF:
			return nil, s.errAt(tok, "expected ';' after upstream_request_id")
		case tokRBrace:
			return nil, s.errAt(tok, "expected ';' after upstream_request_id")
		default:
			return nil, s.errAt(tok, "upstream_request_id expects string header names followed by ';'")
		}
	}
}

func validHeaderFieldName(name string) bool {
	for _, r := range name {
		if r <= 0x20 || r >= 0x7f || !unicode.IsPrint(r) || strings.ContainsRune("()<>@,;:\\\"/[]?={} \t", r) {
			return false
		}
	}
	return true
}
