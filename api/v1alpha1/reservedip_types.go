/*

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type ReservedIPAssignment struct {
	// +optional
	PodName          string `json:"podName,omitempty"`
	PrivateIPAddress string `json:"privateIPAddress,omitempty"`
}

func (r ReservedIPAssignment) MatchesSpec(spec ReservedIPAssignment) bool {
	if spec.PodName == "" {
		return spec.PrivateIPAddress == r.PrivateIPAddress
	}
	return spec.PodName == r.PodName
}

// ReservedIPSpec defines the desired state of EIP
type ReservedIPSpec struct {
	// Which resource this EIP should be assigned to.
	//
	// If not given, it will not be assigned to anything.
	//
	// +optional
	Assignment *ReservedIPAssignment `json:"assignment,omitempty"`

	PublicIPPoolID  string `json:"publicIPPoolID,omitempty"`
	PublicIPAddress string `json:"publicIPAddress,omitempty"`

	// Tags that will be applied to the created EIP.
	// +optional
	Tags *map[string]string `json:"tags,omitempty"`
}

// ReservedIPStatus defines the observed state of EIP
type ReservedIPStatus struct {
	// Current state of the EIP object.
	//
	// State transfer diagram:
	//
	//                   /------- unassigning <----\--------------\
	//                   |                         |              |
	//  *start*:         V                         |              |
	// allocating -> allocated <-> assigning -> assigned <-> reassigning
	//                   |             |
	//   *end*:          |             |
	//  releasing <------/-------------/
	State string `json:"state"`

	OCID            string `json:"OCID,omitempty"`
	PublicIPAddress string `json:"publicIPAddress,omitempty"`

	Assignment         *ReservedIPAssignment `json:"assignment,omitempty"`
	PrivateIPAddressID string                `json:"privateIPAddressID,omitempty"`

	EphemeralIPWasUnassigned bool `json:"ephemeralIPWasUnassigned"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="State",type=string,JSONPath=`.status.state`
// +kubebuilder:printcolumn:name="Public IP",type=string,JSONPath=`.status.publicIPAddress`
// +kubebuilder:printcolumn:name="Private IP",type=string,JSONPath=`.status.assignment.privateIPAddress`
// +kubebuilder:printcolumn:name="Pod",type=string,JSONPath=`.status.assignment.podName`

// ReservedIP is the Schema for the ReservedIPs API
type ReservedIP struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ReservedIPSpec   `json:"spec,omitempty"`
	Status ReservedIPStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ReservedIPList contains a list of ReservedIP
type ReservedIPList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ReservedIP `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ReservedIP{}, &ReservedIPList{})
}
