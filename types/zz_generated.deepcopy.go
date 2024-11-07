//go:build !ignore_autogenerated

// Code generated by controller-gen. DO NOT EDIT.

package types

import (
	"encoding/json"
)

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *Authentication) DeepCopyInto(out *Authentication) {
	*out = *in
	in.Username.DeepCopyInto(&out.Username)
	in.Password.DeepCopyInto(&out.Password)
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new Authentication.
func (in *Authentication) DeepCopy() *Authentication {
	if in == nil {
		return nil
	}
	out := new(Authentication)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *ComponentCheck) DeepCopyInto(out *ComponentCheck) {
	*out = *in
	in.Selector.DeepCopyInto(&out.Selector)
	if in.Inline != nil {
		in, out := &in.Inline, &out.Inline
		*out = make(json.RawMessage, len(*in))
		copy(*out, *in)
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new ComponentCheck.
func (in *ComponentCheck) DeepCopy() *ComponentCheck {
	if in == nil {
		return nil
	}
	out := new(ComponentCheck)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *ConfigMapKeySelector) DeepCopyInto(out *ConfigMapKeySelector) {
	*out = *in
	out.LocalObjectReference = in.LocalObjectReference
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new ConfigMapKeySelector.
func (in *ConfigMapKeySelector) DeepCopy() *ConfigMapKeySelector {
	if in == nil {
		return nil
	}
	out := new(ConfigMapKeySelector)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *ConfigQuery) DeepCopyInto(out *ConfigQuery) {
	*out = *in
	in.ResourceSelector.DeepCopyInto(&out.ResourceSelector)
	if in.Tags != nil {
		in, out := &in.Tags, &out.Tags
		*out = make(map[string]string, len(*in))
		for key, val := range *in {
			(*out)[key] = val
		}
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new ConfigQuery.
func (in *ConfigQuery) DeepCopy() *ConfigQuery {
	if in == nil {
		return nil
	}
	out := new(ConfigQuery)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *EnvVar) DeepCopyInto(out *EnvVar) {
	*out = *in
	if in.ValueFrom != nil {
		in, out := &in.ValueFrom, &out.ValueFrom
		*out = new(EnvVarSource)
		(*in).DeepCopyInto(*out)
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new EnvVar.
func (in *EnvVar) DeepCopy() *EnvVar {
	if in == nil {
		return nil
	}
	out := new(EnvVar)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *EnvVarResourceSelector) DeepCopyInto(out *EnvVarResourceSelector) {
	*out = *in
	out.Agent = in.Agent
	out.ID = in.ID
	out.Name = in.Name
	out.Namespace = in.Namespace
	if in.Types != nil {
		in, out := &in.Types, &out.Types
		*out = make([]ValueExpression, len(*in))
		copy(*out, *in)
	}
	if in.Statuses != nil {
		in, out := &in.Statuses, &out.Statuses
		*out = make([]ValueExpression, len(*in))
		copy(*out, *in)
	}
	out.TagSelector = in.TagSelector
	out.LabelSelector = in.LabelSelector
	out.FieldSelector = in.FieldSelector
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new EnvVarResourceSelector.
func (in *EnvVarResourceSelector) DeepCopy() *EnvVarResourceSelector {
	if in == nil {
		return nil
	}
	out := new(EnvVarResourceSelector)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *EnvVarSource) DeepCopyInto(out *EnvVarSource) {
	*out = *in
	if in.ServiceAccount != nil {
		in, out := &in.ServiceAccount, &out.ServiceAccount
		*out = new(string)
		**out = **in
	}
	if in.HelmRef != nil {
		in, out := &in.HelmRef, &out.HelmRef
		*out = new(HelmRefKeySelector)
		**out = **in
	}
	if in.ConfigMapKeyRef != nil {
		in, out := &in.ConfigMapKeyRef, &out.ConfigMapKeyRef
		*out = new(ConfigMapKeySelector)
		**out = **in
	}
	if in.SecretKeyRef != nil {
		in, out := &in.SecretKeyRef, &out.SecretKeyRef
		*out = new(SecretKeySelector)
		**out = **in
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new EnvVarSource.
func (in *EnvVarSource) DeepCopy() *EnvVarSource {
	if in == nil {
		return nil
	}
	out := new(EnvVarSource)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *Functions) DeepCopyInto(out *Functions) {
	*out = *in
	if in.ComponentConfigTraversal != nil {
		in, out := &in.ComponentConfigTraversal, &out.ComponentConfigTraversal
		*out = new(ComponentConfigTraversalArgs)
		**out = **in
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new Functions.
func (in *Functions) DeepCopy() *Functions {
	if in == nil {
		return nil
	}
	out := new(Functions)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *HTTPBasicAuth) DeepCopyInto(out *HTTPBasicAuth) {
	*out = *in
	in.Authentication.DeepCopyInto(&out.Authentication)
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new HTTPBasicAuth.
func (in *HTTPBasicAuth) DeepCopy() *HTTPBasicAuth {
	if in == nil {
		return nil
	}
	out := new(HTTPBasicAuth)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *HelmRefKeySelector) DeepCopyInto(out *HelmRefKeySelector) {
	*out = *in
	out.LocalObjectReference = in.LocalObjectReference
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new HelmRefKeySelector.
func (in *HelmRefKeySelector) DeepCopy() *HelmRefKeySelector {
	if in == nil {
		return nil
	}
	out := new(HelmRefKeySelector)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *Link) DeepCopyInto(out *Link) {
	*out = *in
	out.Text = in.Text
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new Link.
func (in *Link) DeepCopy() *Link {
	if in == nil {
		return nil
	}
	out := new(Link)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *LocalObjectReference) DeepCopyInto(out *LocalObjectReference) {
	*out = *in
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new LocalObjectReference.
func (in *LocalObjectReference) DeepCopy() *LocalObjectReference {
	if in == nil {
		return nil
	}
	out := new(LocalObjectReference)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *LogSelector) DeepCopyInto(out *LogSelector) {
	*out = *in
	if in.Labels != nil {
		in, out := &in.Labels, &out.Labels
		*out = make(map[string]string, len(*in))
		for key, val := range *in {
			(*out)[key] = val
		}
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new LogSelector.
func (in *LogSelector) DeepCopy() *LogSelector {
	if in == nil {
		return nil
	}
	out := new(LogSelector)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *OAuth) DeepCopyInto(out *OAuth) {
	*out = *in
	in.ClientID.DeepCopyInto(&out.ClientID)
	in.ClientSecret.DeepCopyInto(&out.ClientSecret)
	if in.Scopes != nil {
		in, out := &in.Scopes, &out.Scopes
		*out = make([]string, len(*in))
		copy(*out, *in)
	}
	if in.Params != nil {
		in, out := &in.Params, &out.Params
		*out = make(map[string]string, len(*in))
		for key, val := range *in {
			(*out)[key] = val
		}
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new OAuth.
func (in *OAuth) DeepCopy() *OAuth {
	if in == nil {
		return nil
	}
	out := new(OAuth)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *Property) DeepCopyInto(out *Property) {
	*out = *in
	if in.Value != nil {
		in, out := &in.Value, &out.Value
		*out = new(int64)
		**out = **in
	}
	if in.Max != nil {
		in, out := &in.Max, &out.Max
		*out = new(int64)
		**out = **in
	}
	if in.Min != nil {
		in, out := &in.Min, &out.Min
		*out = new(int64)
		**out = **in
	}
	if in.Links != nil {
		in, out := &in.Links, &out.Links
		*out = make([]Link, len(*in))
		copy(*out, *in)
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new Property.
func (in *Property) DeepCopy() *Property {
	if in == nil {
		return nil
	}
	out := new(Property)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *ResourceSelector) DeepCopyInto(out *ResourceSelector) {
	*out = *in
	in.Functions.DeepCopyInto(&out.Functions)
	if in.Types != nil {
		in, out := &in.Types, &out.Types
		*out = make(Items, len(*in))
		copy(*out, *in)
	}
	if in.Statuses != nil {
		in, out := &in.Statuses, &out.Statuses
		*out = make(Items, len(*in))
		copy(*out, *in)
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new ResourceSelector.
func (in *ResourceSelector) DeepCopy() *ResourceSelector {
	if in == nil {
		return nil
	}
	out := new(ResourceSelector)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *SecretKeySelector) DeepCopyInto(out *SecretKeySelector) {
	*out = *in
	out.LocalObjectReference = in.LocalObjectReference
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new SecretKeySelector.
func (in *SecretKeySelector) DeepCopy() *SecretKeySelector {
	if in == nil {
		return nil
	}
	out := new(SecretKeySelector)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *Summary) DeepCopyInto(out *Summary) {
	*out = *in
	if in.Incidents != nil {
		in, out := &in.Incidents, &out.Incidents
		*out = make(map[string]map[string]int, len(*in))
		for key, val := range *in {
			var outVal map[string]int
			if val == nil {
				(*out)[key] = nil
			} else {
				inVal := (*in)[key]
				in, out := &inVal, &outVal
				*out = make(map[string]int, len(*in))
				for key, val := range *in {
					(*out)[key] = val
				}
			}
			(*out)[key] = outVal
		}
	}
	if in.Insights != nil {
		in, out := &in.Insights, &out.Insights
		*out = make(map[string]map[string]int, len(*in))
		for key, val := range *in {
			var outVal map[string]int
			if val == nil {
				(*out)[key] = nil
			} else {
				inVal := (*in)[key]
				in, out := &inVal, &outVal
				*out = make(map[string]int, len(*in))
				for key, val := range *in {
					(*out)[key] = val
				}
			}
			(*out)[key] = outVal
		}
	}
	if in.Checks != nil {
		in, out := &in.Checks, &out.Checks
		*out = make(map[string]int, len(*in))
		for key, val := range *in {
			(*out)[key] = val
		}
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new Summary.
func (in *Summary) DeepCopy() *Summary {
	if in == nil {
		return nil
	}
	out := new(Summary)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *Text) DeepCopyInto(out *Text) {
	*out = *in
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new Text.
func (in *Text) DeepCopy() *Text {
	if in == nil {
		return nil
	}
	out := new(Text)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *ValueExpression) DeepCopyInto(out *ValueExpression) {
	*out = *in
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new ValueExpression.
func (in *ValueExpression) DeepCopy() *ValueExpression {
	if in == nil {
		return nil
	}
	out := new(ValueExpression)
	in.DeepCopyInto(out)
	return out
}
