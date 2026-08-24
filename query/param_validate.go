package query

import (
	"fmt"
	"strings"
)

// validateParams rejects param declarations that cannot behave as written.
func (p Profile) validateParams() error {
	seen := make(map[string]bool, len(p.Params))
	for _, param := range p.Params {
		if strings.TrimSpace(param.Name) == "" {
			return fmt.Errorf("profile %q declares a param with no name", p.Name)
		}
		if seen[param.Name] {
			return fmt.Errorf("profile %q declares param %q twice", p.Name, param.Name)
		}
		seen[param.Name] = true

		if strings.HasPrefix(param.Name, columnFilterPrefix) {
			return fmt.Errorf(
				"profile %q param %q must not use the %q prefix reserved for column filters",
				p.Name, param.Name, columnFilterPrefix)
		}

		if err := p.validateParamField(param); err != nil {
			return err
		}
		if err := p.validateParamOptions(param); err != nil {
			return err
		}
	}
	return nil
}

func (p Profile) validateParamField(param ParamDef) error {
	if param.Type == ParamTypeList && param.Role != "" && param.Role != ParamRoleFilter {
		return fmt.Errorf(
			"profile %q param %q is a list, which cannot take the %q role", p.Name, param.Name, param.Role)
	}
	if param.Field == "" {
		return nil
	}
	if param.Type != ParamTypeList {
		return fmt.Errorf(
			"profile %q param %q sets field but is type %q; only a list parameter binds to a backend field",
			p.Name, param.Name, param.Type)
	}
	if !SupportsNativeFilters(p.Provider.Type) {
		return fmt.Errorf(
			"profile %q param %q declares field %q, but provider %q applies no native filters, so an excluded value would be silently dropped",
			p.Name, param.Name, param.Field, p.Provider.Type)
	}
	// A param's field is always written by the author, never inferred, so an
	// unusable one is always worth refusing.
	if err := validateSQLFilterField(p.Provider.Type, fmt.Sprintf("profile %q param %q", p.Name, param.Name), param.Field); err != nil {
		return err
	}
	return nil
}

// validateParamOptions rejects static options that cannot survive the
// comma-joined, "!"-excludes wire form a selection travels in.
func (p Profile) validateParamOptions(param ParamDef) error {
	if param.Type != ParamTypeList {
		return nil
	}
	if err := validateFilterOptions(param.Options); err != nil {
		return fmt.Errorf("profile %q param %q: %w", p.Name, param.Name, err)
	}
	return nil
}
