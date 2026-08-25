package query

import (
	"fmt"
	"reflect"
)

type valueVisit struct {
	kind    reflect.Kind
	pointer uintptr
}

func validateAcyclicValue(value any) error {
	return inspectValue(reflect.ValueOf(value), valueVisit{}, map[valueVisit]string{}, "$")
}

func validateContainerValue(container map[string]any, value any) error {
	destination := valueVisit{kind: reflect.Map, pointer: reflect.ValueOf(container).Pointer()}
	return inspectValue(reflect.ValueOf(value), destination, map[valueVisit]string{}, "$")
}

func inspectValue(value reflect.Value, destination valueVisit, stack map[valueVisit]string, path string) error {
	if !value.IsValid() {
		return nil
	}
	for value.Kind() == reflect.Interface {
		if value.IsNil() {
			return nil
		}
		value = value.Elem()
	}

	switch value.Kind() {
	case reflect.Map, reflect.Slice:
		if value.IsNil() {
			return nil
		}
		visit := valueVisit{kind: value.Kind(), pointer: value.Pointer()}
		if visit == destination {
			return fmt.Errorf("value references its destination container at %s", path)
		}
		if firstPath, found := stack[visit]; found {
			return fmt.Errorf("value contains a cycle at %s through %s", path, firstPath)
		}
		stack[visit] = path
		defer delete(stack, visit)
		if value.Kind() == reflect.Map {
			return inspectMap(value, destination, stack, path)
		}
		return inspectSequence(value, destination, stack, path)
	case reflect.Array:
		return inspectSequence(value, destination, stack, path)
	case reflect.Pointer:
		return inspectPointer(value, destination, stack, path)
	default:
		return nil
	}
}

func inspectMap(value reflect.Value, destination valueVisit, stack map[valueVisit]string, path string) error {
	iterator := value.MapRange()
	for iterator.Next() {
		childPath := path + "[*]"
		if iterator.Key().Kind() == reflect.String {
			childPath = path + "." + iterator.Key().String()
		}
		if err := inspectValue(iterator.Value(), destination, stack, childPath); err != nil {
			return err
		}
	}
	return nil
}

func inspectSequence(value reflect.Value, destination valueVisit, stack map[valueVisit]string, path string) error {
	for index := range value.Len() {
		if err := inspectValue(value.Index(index), destination, stack, fmt.Sprintf("%s[%d]", path, index)); err != nil {
			return err
		}
	}
	return nil
}

func inspectPointer(value reflect.Value, destination valueVisit, stack map[valueVisit]string, path string) error {
	if value.IsNil() {
		return nil
	}
	switch value.Elem().Kind() {
	case reflect.Array, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		visit := valueVisit{kind: value.Kind(), pointer: value.Pointer()}
		if firstPath, found := stack[visit]; found {
			return fmt.Errorf("value contains a cycle at %s through %s", path, firstPath)
		}
		stack[visit] = path
		defer delete(stack, visit)
		return inspectValue(value.Elem(), destination, stack, path)
	default:
		return nil
	}
}
