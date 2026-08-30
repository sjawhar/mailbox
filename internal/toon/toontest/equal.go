package toontest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"strconv"
	"strings"
)

// EqualJSON reports semantic equality between v and one JSON document,
// per TOON §2 JSON-model equality (ordered objects, numeric value equality).
func EqualJSON(v Value, jsonDoc []byte) error {
	dec := json.NewDecoder(bytes.NewReader(jsonDoc))
	dec.UseNumber()
	want, err := decodeJSONValue(dec)
	if err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("JSON oracle: trailing content")
		}
		return err
	}
	return equalAt(v, want, "$")
}

func decodeJSONValue(dec *json.Decoder) (Value, error) {
	token, err := dec.Token()
	if err != nil {
		return Value{}, err
	}
	switch token := token.(type) {
	case nil:
		return Value{Kind: Null}, nil
	case bool:
		return Value{Kind: Bool, Bool: token}, nil
	case string:
		return Value{Kind: String, Str: token}, nil
	case json.Number:
		return Value{Kind: Number, Num: token.String()}, nil
	case json.Delim:
		switch token {
		case '[':
			values := make([]Value, 0)
			for dec.More() {
				value, err := decodeJSONValue(dec)
				if err != nil {
					return Value{}, err
				}
				values = append(values, value)
			}
			_, err := dec.Token()
			return Value{Kind: Array, Arr: values}, err
		case '{':
			fields := make([]Field, 0)
			for dec.More() {
				key, err := dec.Token()
				if err != nil {
					return Value{}, err
				}
				value, err := decodeJSONValue(dec)
				if err != nil {
					return Value{}, err
				}
				fields = append(fields, Field{Key: key.(string), Val: value})
			}
			_, err := dec.Token()
			return Value{Kind: Object, Obj: fields}, err
		}
	}
	return Value{}, fmt.Errorf("JSON oracle: unsupported token")
}

func equalAt(got, want Value, path string) error {
	if got.Kind != want.Kind {
		return fmt.Errorf("%s: kind %v, want %v", path, got.Kind, want.Kind)
	}
	switch got.Kind {
	case Null:
		return nil
	case Bool:
		if got.Bool != want.Bool {
			return fmt.Errorf("%s: bool %v, want %v", path, got.Bool, want.Bool)
		}
	case String:
		if got.Str != want.Str {
			return fmt.Errorf("%s: string %q, want %q", path, got.Str, want.Str)
		}
	case Number:
		if !numbersEqual(got.Num, want.Num) {
			return fmt.Errorf("%s: number %s, want %s", path, got.Num, want.Num)
		}
	case Array:
		if len(got.Arr) != len(want.Arr) {
			return fmt.Errorf("%s: array length %d, want %d", path, len(got.Arr), len(want.Arr))
		}
		for i := range got.Arr {
			if err := equalAt(got.Arr[i], want.Arr[i], fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case Object:
		if len(got.Obj) != len(want.Obj) {
			return fmt.Errorf("%s: object field count %d, want %d", path, len(got.Obj), len(want.Obj))
		}
		if got.relaxedOrder {
			for _, field := range got.Obj {
				matched := -1
				for i, candidate := range want.Obj {
					if candidate.Key == field.Key {
						matched = i
						break
					}
				}
				if matched < 0 {
					return fmt.Errorf("%s: missing field %q", path, field.Key)
				}
				if err := equalAt(field.Val, want.Obj[matched].Val, path+"."+field.Key); err != nil {
					return err
				}
			}
			return nil
		}
		for i := range got.Obj {
			if got.Obj[i].Key != want.Obj[i].Key {
				return fmt.Errorf("%s: field %d is %q, want %q", path, i, got.Obj[i].Key, want.Obj[i].Key)
			}
			if err := equalAt(got.Obj[i].Val, want.Obj[i].Val, path+"."+got.Obj[i].Key); err != nil {
				return err
			}
		}
	}
	return nil
}

func numbersEqual(left, right string) bool {
	l, lok := numberRat(left)
	r, rok := numberRat(right)
	return lok && rok && l.Cmp(r) == 0
}

func numberRat(token string) (*big.Rat, bool) {
	sign := 1
	if strings.HasPrefix(token, "-") {
		sign = -1
		token = token[1:]
	}
	exponent := 0
	if index := strings.IndexAny(token, "eE"); index >= 0 {
		parsed, err := strconv.Atoi(token[index+1:])
		if err != nil {
			return nil, false
		}
		exponent = parsed
		token = token[:index]
	}
	fractionDigits := 0
	if index := strings.IndexByte(token, '.'); index >= 0 {
		fractionDigits = len(token) - index - 1
		token = token[:index] + token[index+1:]
	}
	coefficient := new(big.Int)
	if _, ok := coefficient.SetString(token, 10); !ok {
		return nil, false
	}
	if sign < 0 {
		coefficient.Neg(coefficient)
	}
	scale := fractionDigits - exponent
	if scale < 0 {
		coefficient.Mul(coefficient, pow10(-scale))
		return new(big.Rat).SetInt(coefficient), true
	}
	return new(big.Rat).SetFrac(coefficient, pow10(scale)), true
}

func pow10(n int) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
}
