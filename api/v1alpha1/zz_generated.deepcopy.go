// Hand-written deep copy. Equivalent to what controller-gen object would
// produce; we keep it hand-rolled so the codegen toolchain is not a
// build-time dependency. hack/update-codegen.sh re-validates the structure
// when controller-gen is available.

package v1alpha1

import (
	"github.com/awslabs/operatorpkg/status"
	runtime "k8s.io/apimachinery/pkg/runtime"
)

// DeepCopyInto copies all fields from in to out, including pointer indirections.
func (in *ScalewayNodeClass) DeepCopyInto(out *ScalewayNodeClass) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

func (in *ScalewayNodeClass) DeepCopy() *ScalewayNodeClass {
	if in == nil {
		return nil
	}
	out := new(ScalewayNodeClass)
	in.DeepCopyInto(out)
	return out
}

func (in *ScalewayNodeClass) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *ScalewayNodeClassList) DeepCopyInto(out *ScalewayNodeClassList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		inItems, outItems := &in.Items, &out.Items
		*outItems = make([]ScalewayNodeClass, len(*inItems))
		for i := range *inItems {
			(*inItems)[i].DeepCopyInto(&(*outItems)[i])
		}
	}
}

func (in *ScalewayNodeClassList) DeepCopy() *ScalewayNodeClassList {
	if in == nil {
		return nil
	}
	out := new(ScalewayNodeClassList)
	in.DeepCopyInto(out)
	return out
}

func (in *ScalewayNodeClassList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *ScalewayNodeClassSpec) DeepCopyInto(out *ScalewayNodeClassSpec) {
	*out = *in
	if in.Zones != nil {
		out.Zones = make([]string, len(in.Zones))
		copy(out.Zones, in.Zones)
	}
	if in.Tags != nil {
		out.Tags = make(map[string]string, len(in.Tags))
		for k, v := range in.Tags {
			out.Tags[k] = v
		}
	}
	if in.RootVolumeSizeGB != nil {
		v := *in.RootVolumeSizeGB
		out.RootVolumeSizeGB = &v
	}
	if in.PublicIP != nil {
		v := *in.PublicIP
		out.PublicIP = &v
	}
}

func (in *ScalewayNodeClassSpec) DeepCopy() *ScalewayNodeClassSpec {
	if in == nil {
		return nil
	}
	out := new(ScalewayNodeClassSpec)
	in.DeepCopyInto(out)
	return out
}

func (in *ScalewayNodeClassStatus) DeepCopyInto(out *ScalewayNodeClassStatus) {
	*out = *in
	if in.Conditions != nil {
		inC, outC := &in.Conditions, &out.Conditions
		*outC = make([]status.Condition, len(*inC))
		copy(*outC, *inC)
	}
	if in.UnusedFieldsForBackend != nil {
		out.UnusedFieldsForBackend = append([]string(nil), in.UnusedFieldsForBackend...)
	}
	if in.ValidatedAt != nil {
		out.ValidatedAt = in.ValidatedAt.DeepCopy()
	}
	if in.ResolvedZones != nil {
		out.ResolvedZones = make([]string, len(in.ResolvedZones))
		copy(out.ResolvedZones, in.ResolvedZones)
	}
}

func (in *ScalewayNodeClassStatus) DeepCopy() *ScalewayNodeClassStatus {
	if in == nil {
		return nil
	}
	out := new(ScalewayNodeClassStatus)
	in.DeepCopyInto(out)
	return out
}
