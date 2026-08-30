package toon

import (
	"strconv"
	"strings"
)

type column struct {
	key      string
	children []column
}

func encodeDocument(v value) string {
	var lines []string
	switch v.kind {
	case kindObject:
		if columns, ok := keyedColumns(v); ok {
			lines = appendKeyedTable(lines, "", false, v, columns, 0)
		} else {
			lines = appendObjectFields(lines, v.obj, 0)
		}
	case kindArray:
		lines = appendRootArray(lines, v)
	default:
		lines = append(lines, encodePrimitive(v))
	}
	return strings.Join(lines, "\n")
}

func appendRootArray(lines []string, v value) []string {
	if len(v.arr) == 0 {
		return append(lines, "[]")
	}
	if allPrimitives(v.arr) {
		return append(lines, header("", len(v.arr), false, nil)+" "+joinPrimitives(v.arr))
	}
	if columns, ok := tabularColumns(v.arr); ok {
		return appendTable(lines, "", false, v.arr, columns, 0)
	}
	lines = append(lines, header("", len(v.arr), false, nil))
	return appendArrayItems(lines, v.arr, 1)
}

func appendObjectFields(lines []string, fields []field, depth int) []string {
	for _, f := range fields {
		lines = appendField(lines, f.key, f.val, depth)
	}
	return lines
}

func appendField(lines []string, key string, v value, depth int) []string {
	prefix := indent(depth) + encodeKey(key)
	switch v.kind {
	case kindNull, kindBool, kindNumber, kindString:
		return append(lines, prefix+": "+encodePrimitive(v))
	case kindObject:
		if columns, ok := keyedColumns(v); ok {
			return appendKeyedTable(lines, key, true, v, columns, depth)
		}
		lines = append(lines, prefix+":")
		return appendObjectFields(lines, v.obj, depth+1)
	case kindArray:
		if len(v.arr) == 0 {
			return append(lines, prefix+": []")
		}
		if allPrimitives(v.arr) {
			return append(lines, prefix+header("", len(v.arr), false, nil)+" "+joinPrimitives(v.arr))
		}
		if columns, ok := tabularColumns(v.arr); ok {
			return appendTable(lines, key, true, v.arr, columns, depth)
		}
		lines = append(lines, prefix+header("", len(v.arr), false, nil))
		return appendArrayItems(lines, v.arr, depth+1)
	default:
		panic("TOON: unsupported field value")
	}
}

func appendTable(lines []string, key string, hasKey bool, values []value, columns []column, depth int) []string {
	line := header("", len(values), false, columns)
	if hasKey {
		line = encodeKey(key) + line
	}
	lines = append(lines, indent(depth)+line)
	for _, v := range values {
		lines = append(lines, indent(depth+1)+joinLeaves(v, columns))
	}
	return lines
}

func appendKeyedTable(lines []string, key string, hasKey bool, v value, columns []column, depth int) []string {
	line := header("", len(v.obj), true, columns)
	if hasKey {
		line = encodeKey(key) + line
	}
	lines = append(lines, indent(depth)+line)
	for _, entry := range v.obj {
		lines = append(lines, indent(depth+1)+encodeKey(entry.key)+": "+joinLeaves(entry.val, columns))
	}
	return lines
}
func appendArrayItems(lines []string, values []value, depth int) []string {
	for _, v := range values {
		lines = appendArrayItem(lines, v, depth)
	}
	return lines
}

func appendArrayItem(lines []string, v value, depth int) []string {
	prefix := indent(depth) + "-"
	switch v.kind {
	case kindNull, kindBool, kindNumber, kindString:
		return append(lines, prefix+" "+encodePrimitive(v))
	case kindArray:
		if len(v.arr) == 0 {
			return append(lines, prefix+" [0]:")
		}
		if allPrimitives(v.arr) {
			return append(lines, prefix+" "+header("", len(v.arr), false, nil)+" "+joinPrimitives(v.arr))
		}
		lines = append(lines, prefix+" "+header("", len(v.arr), false, nil))
		return appendArrayItems(lines, v.arr, depth+1)
	case kindObject:
		return appendObjectItem(lines, v, depth)
	default:
		panic("TOON: unsupported array item")
	}
}

func appendObjectItem(lines []string, v value, depth int) []string {
	if len(v.obj) == 0 {
		return append(lines, indent(depth)+"-")
	}
	first := v.obj[0]
	prefix := indent(depth) + "- " + encodeKey(first.key)
	switch first.val.kind {
	case kindNull, kindBool, kindNumber, kindString:
		lines = append(lines, prefix+": "+encodePrimitive(first.val))
	case kindObject:
		if columns, ok := keyedColumns(first.val); ok {
			lines = append(lines, prefix+header("", len(first.val.obj), true, columns))
			for _, entry := range first.val.obj {
				lines = append(lines, indent(depth+2)+encodeKey(entry.key)+": "+joinLeaves(entry.val, columns))
			}
		} else {
			lines = append(lines, prefix+":")
			lines = appendObjectFields(lines, first.val.obj, depth+2)
		}
	case kindArray:
		if len(first.val.arr) == 0 {
			lines = append(lines, prefix+": []")
		} else if allPrimitives(first.val.arr) {
			lines = append(lines, prefix+header("", len(first.val.arr), false, nil)+" "+joinPrimitives(first.val.arr))
		} else if columns, ok := tabularColumns(first.val.arr); ok {
			lines = append(lines, prefix+header("", len(first.val.arr), false, columns))
			for _, row := range first.val.arr {
				lines = append(lines, indent(depth+2)+joinLeaves(row, columns))
			}
		} else {
			lines = append(lines, prefix+header("", len(first.val.arr), false, nil))
			lines = appendArrayItems(lines, first.val.arr, depth+2)
		}
	default:
		panic("TOON: unsupported list-item field")
	}
	return appendObjectFields(lines, v.obj[1:], depth+1)
}

func header(key string, length int, keyed bool, columns []column) string {
	var b strings.Builder
	if key != "" {
		b.WriteString(encodeKey(key))
	}
	b.WriteByte('[')
	b.WriteString(strconv.Itoa(length))
	if keyed {
		b.WriteByte(':')
	}
	b.WriteByte(']')
	if len(columns) > 0 {
		b.WriteByte('{')
		b.WriteString(renderColumns(columns))
		b.WriteByte('}')
	}
	b.WriteByte(':')
	return b.String()
}

func renderColumns(columns []column) string {
	parts := make([]string, len(columns))
	for i, col := range columns {
		parts[i] = encodeKey(col.key)
		if len(col.children) > 0 {
			parts[i] += "{" + renderColumns(col.children) + "}"
		}
	}
	return strings.Join(parts, ",")
}

func joinPrimitives(values []value) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = encodePrimitive(v)
	}
	return strings.Join(parts, ",")
}

func joinLeaves(v value, columns []column) string {
	leaves := make([]string, 0, leafCount(columns))
	appendLeaves(&leaves, v, columns)
	return strings.Join(leaves, ",")
}

func appendLeaves(leaves *[]string, v value, columns []column) {
	for _, col := range columns {
		child, ok := objectField(v, col.key)
		if !ok {
			panic("TOON: tabular field missing")
		}
		if len(col.children) == 0 {
			*leaves = append(*leaves, encodePrimitive(child))
			continue
		}
		appendLeaves(leaves, child, col.children)
	}
}

func leafCount(columns []column) int {
	n := 0
	for _, col := range columns {
		if len(col.children) == 0 {
			n++
		} else {
			n += leafCount(col.children)
		}
	}
	return n
}

func allPrimitives(values []value) bool {
	for _, v := range values {
		if !isPrimitive(v) {
			return false
		}
	}
	return true
}

func isPrimitive(v value) bool {
	return v.kind == kindNull || v.kind == kindBool || v.kind == kindNumber || v.kind == kindString
}

func tabularColumns(values []value) ([]column, bool) {
	if len(values) == 0 {
		return nil, false
	}
	objects := make([]value, len(values))
	for i, v := range values {
		if v.kind != kindObject {
			return nil, false
		}
		objects[i] = v
	}
	return uniformObjectColumns(objects)
}

func keyedColumns(v value) ([]column, bool) {
	if v.kind != kindObject || len(v.obj) < 2 {
		return nil, false
	}
	objects := make([]value, len(v.obj))
	for i, entry := range v.obj {
		if entry.val.kind != kindObject {
			return nil, false
		}
		objects[i] = entry.val
	}
	return uniformObjectColumns(objects)
}

func uniformObjectColumns(objects []value) ([]column, bool) {
	if len(objects) == 0 || len(objects[0].obj) == 0 {
		return nil, false
	}
	first := objects[0]
	for _, object := range objects {
		if object.kind != kindObject || !sameObjectKeys(first, object) {
			return nil, false
		}
	}
	columns := make([]column, 0, len(first.obj))
	for _, firstField := range first.obj {
		values := make([]value, len(objects))
		for i, object := range objects {
			value, _ := objectField(object, firstField.key)
			values[i] = value
		}
		if allPrimitives(values) {
			columns = append(columns, column{key: firstField.key})
			continue
		}
		children, ok := uniformObjectColumns(values)
		if !ok {
			return nil, false
		}
		columns = append(columns, column{key: firstField.key, children: children})
	}
	return columns, true
}

func sameObjectKeys(first, other value) bool {
	if len(first.obj) != len(other.obj) {
		return false
	}
	for _, f := range first.obj {
		if _, ok := objectField(other, f.key); !ok {
			return false
		}
	}
	return true
}

func objectField(v value, key string) (value, bool) {
	for _, f := range v.obj {
		if f.key == key {
			return f.val, true
		}
	}
	return value{}, false
}

func indent(depth int) string {
	return strings.Repeat("  ", depth)
}
