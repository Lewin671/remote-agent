// Package text provides typed constructors and generated message keys for corev1.Text.
package text

import (
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
)

// Plain constructs a literal Text rendered verbatim without translation.
func Plain(s string) *corev1.Text {
	if s == "" {
		return nil
	}
	return &corev1.Text{Literal: s}
}

// LocalizedPlain constructs a literal Text with distinct Chinese and English renderings
// (e.g. "1.2万" vs "12k"). When en is empty or equal to zh, only Literal is populated.
func LocalizedPlain(zh, en string) *corev1.Text {
	if zh == "" && en == "" {
		return nil
	}
	if en == "" || en == zh {
		return &corev1.Text{Literal: zh}
	}
	return &corev1.Text{Literal: zh, LiteralEn: en}
}

// Msg constructs a localizable Text referencing key with optional substitution args.
func Msg(key string, args ...*corev1.TextArg) *corev1.Text {
	if key == "" {
		return nil
	}
	return &corev1.Text{
		Key:  key,
		Args: args,
	}
}

// Str constructs a string TextArg.
func Str(name, val string) *corev1.TextArg {
	return &corev1.TextArg{
		Name:  name,
		Value: &corev1.TextArg_StrValue{StrValue: val},
	}
}

// Int constructs an integer TextArg.
func Int(name string, val int64) *corev1.TextArg {
	return &corev1.TextArg{
		Name:  name,
		Value: &corev1.TextArg_IntValue{IntValue: val},
	}
}

// Num constructs a floating-point TextArg.
func Num(name string, val float64) *corev1.TextArg {
	return &corev1.TextArg{
		Name:  name,
		Value: &corev1.TextArg_FloatValue{FloatValue: val},
	}
}

// ArgText constructs a nested localizable TextArg.
func ArgText(name string, val *corev1.Text) *corev1.TextArg {
	return &corev1.TextArg{
		Name:  name,
		Value: &corev1.TextArg_TextValue{TextValue: val},
	}
}

// Time constructs a standalone timestamp Text formatted by the shell in its local timezone and locale.
func Time(unixMs int64, style corev1.TimestampStyle) *corev1.Text {
	if unixMs <= 0 {
		return nil
	}
	return &corev1.Text{
		Timestamp: &corev1.TimestampArg{
			UnixMs: unixMs,
			Style:  style,
		},
	}
}

// ArgTimestamp constructs a timestamp TextArg formatted by the shell in its local timezone and locale.
func ArgTimestamp(name string, unixMs int64, style corev1.TimestampStyle) *corev1.TextArg {
	return &corev1.TextArg{
		Name: name,
		Value: &corev1.TextArg_TimestampValue{
			TimestampValue: &corev1.TimestampArg{
				UnixMs: unixMs,
				Style:  style,
			},
		},
	}
}

// Join joins non-empty Text segments using a localizable separator message key.
func Join(separatorKey string, parts ...*corev1.Text) *corev1.Text {
	filtered := filterNonEmpty(parts)
	if len(filtered) == 0 {
		return nil
	}
	if len(filtered) == 1 {
		return filtered[0]
	}
	return &corev1.Text{
		Join:         filtered,
		SeparatorKey: separatorKey,
	}
}

// JoinLiteral joins non-empty Text segments using an unlocalized literal separator (such as " · ").
func JoinLiteral(sep string, parts ...*corev1.Text) *corev1.Text {
	filtered := filterNonEmpty(parts)
	if len(filtered) == 0 {
		return nil
	}
	if len(filtered) == 1 {
		return filtered[0]
	}
	return &corev1.Text{
		Join:             filtered,
		SeparatorLiteral: sep,
	}
}

// SpokenJoin joins non-empty Text segments for screen readers using common.ios_spoken_separator.
func SpokenJoin(parts ...*corev1.Text) *corev1.Text {
	return Join(KeyCommonIosSpokenSeparator, parts...)
}

// IsEmpty reports whether t contains no message key, literal text, timestamp, or join segments.
func IsEmpty(t *corev1.Text) bool {
	if t == nil {
		return true
	}
	return t.Key == "" && t.Literal == "" && t.LiteralEn == "" && len(t.Join) == 0 && t.Timestamp == nil
}

func filterNonEmpty(parts []*corev1.Text) []*corev1.Text {
	var out []*corev1.Text
	for _, p := range parts {
		if !IsEmpty(p) {
			out = append(out, p)
		}
	}
	return out
}
