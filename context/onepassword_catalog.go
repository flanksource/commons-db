package context

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
)

var onePasswordID = regexp.MustCompile(`^[a-z0-9]{26}$`)

type OnePasswordVault struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type OnePasswordItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type OnePasswordField struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Reference string `json:"reference"`
	Section   string `json:"section,omitempty"`
}

func ListOnePasswordVaults(ctx Context) ([]OnePasswordVault, error) {
	payload, err := onePasswordCommandFunc(ctx, onePasswordToken(ctx), "vault", "list", "--format=json")
	if err != nil {
		return nil, fmt.Errorf("list 1password vaults: %w", err)
	}
	var vaults []OnePasswordVault
	if err := json.Unmarshal(payload, &vaults); err != nil {
		return nil, fmt.Errorf("decode 1password vault metadata: %w", err)
	}
	for _, vault := range vaults {
		if err := validateOnePasswordCatalogEntry("vault", vault.ID, vault.Name); err != nil {
			return nil, err
		}
	}
	sort.Slice(vaults, func(i, j int) bool {
		if vaults[i].Name == vaults[j].Name {
			return vaults[i].ID < vaults[j].ID
		}
		return vaults[i].Name < vaults[j].Name
	})
	return vaults, nil
}

func ListOnePasswordItems(ctx Context, vaultID string) ([]OnePasswordItem, error) {
	if err := validateOnePasswordID("vault", vaultID); err != nil {
		return nil, err
	}
	payload, err := onePasswordCommandFunc(ctx, onePasswordToken(ctx), "item", "list", "--vault", vaultID, "--format=json")
	if err != nil {
		return nil, fmt.Errorf("list 1password items in vault %q: %w", vaultID, err)
	}
	var records []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal(payload, &records); err != nil {
		return nil, fmt.Errorf("decode 1password item metadata: %w", err)
	}
	items := make([]OnePasswordItem, 0, len(records))
	for _, record := range records {
		if record.Title == "" {
			continue
		}
		if err := validateOnePasswordCatalogEntry("item", record.ID, record.Title); err != nil {
			return nil, err
		}
		items = append(items, OnePasswordItem{ID: record.ID, Name: record.Title})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Name == items[j].Name {
			return items[i].ID < items[j].ID
		}
		return items[i].Name < items[j].Name
	})
	return items, nil
}

func ListOnePasswordFields(ctx Context, vaultID, itemID string) ([]OnePasswordField, error) {
	if err := validateOnePasswordID("vault", vaultID); err != nil {
		return nil, err
	}
	if err := validateOnePasswordID("item", itemID); err != nil {
		return nil, err
	}
	payload, err := onePasswordCommandFunc(ctx, onePasswordToken(ctx), "item", "get", itemID, "--vault", vaultID, "--format=json")
	if err != nil {
		return nil, fmt.Errorf("get 1password item %q: %w", itemID, err)
	}
	var record struct {
		Fields []struct {
			ID        string `json:"id"`
			Label     string `json:"label"`
			Reference string `json:"reference"`
			Section   *struct {
				Label string `json:"label"`
			} `json:"section"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(payload, &record); err != nil {
		return nil, fmt.Errorf("decode 1password field metadata: %w", err)
	}
	fields := make([]OnePasswordField, 0, len(record.Fields))
	for _, field := range record.Fields {
		if field.Reference == "" {
			continue
		}
		if field.ID == "" || field.Label == "" {
			return nil, fmt.Errorf("invalid 1password field metadata: id and label are required")
		}
		if err := validateOnePasswordReference(field.Reference); err != nil {
			return nil, fmt.Errorf("invalid 1password field %q reference: %w", field.ID, err)
		}
		metadata := OnePasswordField{ID: field.ID, Label: field.Label, Reference: field.Reference}
		if field.Section != nil {
			metadata.Section = field.Section.Label
		}
		fields = append(fields, metadata)
	}
	sort.Slice(fields, func(i, j int) bool {
		if fields[i].Section != fields[j].Section {
			return fields[i].Section < fields[j].Section
		}
		if fields[i].Label == fields[j].Label {
			return fields[i].ID < fields[j].ID
		}
		return fields[i].Label < fields[j].Label
	})
	return fields, nil
}

func validateOnePasswordID(kind, id string) error {
	if !onePasswordID.MatchString(id) {
		return fmt.Errorf("invalid 1password %s ID %q", kind, id)
	}
	return nil
}

func validateOnePasswordCatalogEntry(kind, id, name string) error {
	if err := validateOnePasswordID(kind, id); err != nil {
		return err
	}
	if name == "" {
		return fmt.Errorf("invalid 1password %s metadata: name is required", kind)
	}
	return nil
}
