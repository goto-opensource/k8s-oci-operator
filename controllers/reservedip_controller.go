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

package controllers

import (
	"context"
	"fmt"
	"net"
	"reflect"
	"strings"

	"github.com/go-logr/logr"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"

	ocicommon "github.com/oracle/oci-go-sdk/v31/common"
	ocicore "github.com/oracle/oci-go-sdk/v31/core"

	ociv1alpha1 "github.com/logmein/k8s-oci-operator/api/v1alpha1"
)

// ReservedIPReconciler reconciles a EIP object
type ReservedIPReconciler struct {
	client.Client
	Log                  logr.Logger
	Recorder             record.EventRecorder
	VNC                  *ocicore.VirtualNetworkClient
	CompartmentID        string
	VcnID                string
	ReservedIPNamePrefix string
}

// +kubebuilder:rbac:groups=oci.k8s.logmein.com,resources=reservedips,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=oci.k8s.logmein.com,resources=reservedips/status,verbs=get;update;patch

func (r *ReservedIPReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("reservedIP", req.NamespacedName)

	var reservedIP ociv1alpha1.ReservedIP
	if err := r.Get(ctx, req.NamespacedName, &reservedIP); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	res, err := r.handleRequest(ctx, &reservedIP, log)
	if err != nil {
		r.Recorder.Event(&reservedIP, "Warning", "ReconcileError", err.Error())
	}
	return res, err
}

func (r *ReservedIPReconciler) handleRequest(ctx context.Context, reservedIP *ociv1alpha1.ReservedIP, log logr.Logger) (ctrl.Result, error) {
	status := &reservedIP.Status
	spec := &reservedIP.Spec

	if reservedIP.ObjectMeta.DeletionTimestamp.IsZero() {
		if !containsString(reservedIP.ObjectMeta.Finalizers, finalizerName) {
			// add finalizer, set initial state
			reservedIP.ObjectMeta.Finalizers = append(reservedIP.ObjectMeta.Finalizers, finalizerName)
			return ctrl.Result{}, r.Update(ctx, reservedIP)
		}

		if status.State == "" {
			status.State = "allocating"
			if err := r.Status().Update(ctx, reservedIP); err != nil {
				return ctrl.Result{}, err
			}
		}

		if status.State == "allocating" {
			if err := r.allocateReservedIP(ctx, reservedIP, log); err != nil {
				return ctrl.Result{}, err
			}
			r.Recorder.Event(reservedIP, "Normal", "Allocating", "Reserved IP allocated")
		}

		addr, err := r.VNC.GetPublicIp(ctx, ocicore.GetPublicIpRequest{
			PublicIpId: &status.OCID,
		})
		if err != nil {
			if strings.Contains(err.Error(), "NotAuthorizedOrNotFound") {
				log.Info("allocation ID not found; assuming EIP was released; not doing anything", "ocid", reservedIP.Status.OCID)
			}
			return ctrl.Result{}, err
		}
		log = log.WithValues("publicIPID", addr.Id)

		if err := r.reconcileTags(ctx, reservedIP, addr.FreeformTags); err != nil {
			return ctrl.Result{}, err
		}

		if status.State == "allocated" {
			if spec.Assignment != nil {
				if spec.Assignment.PodName != "" || spec.Assignment.PrivateIPAddress != "" {
					status.State = "assigning"
					if err := r.Status().Update(ctx, reservedIP); err != nil {
						return ctrl.Result{}, err
					}
					r.Recorder.Event(reservedIP, "Normal", "Assigning", "Reserved IP assigned")
				}
			}
		}

		if status.State == "assigned" {
			changed := false
			if spec.Assignment == nil {
				// assignment was removed
				status.State = "unassigning"
				changed = true
			} else if !status.Assignment.MatchesSpec(*spec.Assignment) || addr.AssignedEntityId == nil {
				// assignment was changed (in spec or in EC2)
				status.State = "reassigning"
				changed = true
			}

			if changed {
				if err := r.Status().Update(ctx, reservedIP); err != nil {
					return ctrl.Result{}, err
				}
			}
		}

		if status.State == "assigning" {
			if spec.Assignment == nil {
				// assignment was removed before EIP was actually assigned
				status.State = "allocated"
				if err := r.Status().Update(ctx, reservedIP); err != nil {
					return ctrl.Result{}, err
				}
			}
		}

		if status.State == "assigning" || status.State == "reassigning" {
			if spec.Assignment != nil {
				if spec.Assignment.PodName != "" || spec.Assignment.PrivateIPAddress != "" {
					return ctrl.Result{}, r.assignReservedIP(ctx, reservedIP, log)
				}
			}
		}

		if status.State == "unassigning" {
			return ctrl.Result{}, r.unassignReservedIP(ctx, reservedIP, log)
		}
	} else {
		// EIP object is being deleted
		if containsString(reservedIP.ObjectMeta.Finalizers, finalizerName) {
			if status.OCID != "" {
				if status.State != "releasing" {
					status.State = "releasing"
					if err := r.Status().Update(ctx, reservedIP); err != nil {
						return ctrl.Result{}, err
					}
				}

				if status.State == "releasing" {
					if err := r.releaseReservedIP(ctx, reservedIP, log); err != nil {
						return ctrl.Result{}, err
					}
				}
			}

			// remove finalizer, allow k8s to remove the resource
			reservedIP.ObjectMeta.Finalizers = removeString(reservedIP.ObjectMeta.Finalizers, finalizerName)
			return ctrl.Result{}, r.Update(ctx, reservedIP)
		}
	}

	return ctrl.Result{}, nil
}

func (r *ReservedIPReconciler) allocateReservedIP(ctx context.Context, reservedIP *ociv1alpha1.ReservedIP, log logr.Logger) error {
	log.Info("allocating")

	displayName := fmt.Sprintf("%s-%s-%s-%s", r.ReservedIPNamePrefix, reservedIP.Namespace, reservedIP.Name, reservedIP.UID)

	input := ocicore.CreatePublicIpRequest{
		CreatePublicIpDetails: ocicore.CreatePublicIpDetails{
			CompartmentId: ocicommon.String(r.CompartmentID),
			DisplayName:   ocicommon.String(displayName),
			Lifetime:      ocicore.CreatePublicIpDetailsLifetimeReserved,
		},
		OpcRetryToken: ocicommon.String(string(reservedIP.UID)),
	}
	if reservedIP.Spec.Tags != nil {
		input.FreeformTags = *reservedIP.Spec.Tags
	}
	if reservedIP.Spec.PublicIPPoolID != "" {
		input.PublicIpPoolId = ocicommon.String(reservedIP.Spec.PublicIPPoolID)
	}

	resp, err := r.VNC.CreatePublicIp(ctx, input)
	if err != nil {
		return err
	}

	reservedIP.Status.State = "allocated"
	reservedIP.Status.OCID = *resp.Id
	reservedIP.Status.PublicIPAddress = *resp.IpAddress
	r.Log.Info("allocated", "ocid", reservedIP.Status.OCID)
	if err := r.Status().Update(ctx, reservedIP); err != nil {
		return err
	}

	return r.reconcileTags(ctx, reservedIP, resp.FreeformTags)
}

func (r *ReservedIPReconciler) reconcileTags(ctx context.Context, reservedIP *ociv1alpha1.ReservedIP, existingTags map[string]string) error {
	if reservedIP.Spec.Tags != nil && !reflect.DeepEqual(*reservedIP.Spec.Tags, existingTags) {
		_, err := r.VNC.UpdatePublicIp(ctx, ocicore.UpdatePublicIpRequest{
			PublicIpId: ocicommon.String(reservedIP.Status.OCID),
			UpdatePublicIpDetails: ocicore.UpdatePublicIpDetails{
				FreeformTags: *reservedIP.Spec.Tags,
			},
		})
		return err
	}
	return nil
}

func (r *ReservedIPReconciler) assignEphemeralIP(ctx context.Context, reservedIP *ociv1alpha1.ReservedIP, log logr.Logger) error {
	log.Info("ephemeral IP was unassigned for assigning ReservedIP; assigning a new ephemeral IP")

	input := ocicore.CreatePublicIpRequest{
		CreatePublicIpDetails: ocicore.CreatePublicIpDetails{
			CompartmentId: ocicommon.String(r.CompartmentID),
			PrivateIpId:   ocicommon.String(reservedIP.Status.PrivateIPAddressID),
			Lifetime:      ocicore.CreatePublicIpDetailsLifetimeEphemeral,
		},
	}
	if _, err := r.VNC.CreatePublicIp(ctx, input); err != nil {
		return err
	}

	reservedIP.Status.EphemeralIPWasUnassigned = false
	if err := r.Status().Update(ctx, reservedIP); err != nil {
		return err
	}

	return nil
}

func (r *ReservedIPReconciler) releaseReservedIP(ctx context.Context, eip *ociv1alpha1.ReservedIP, log logr.Logger) error {
	log.Info("releasing")

	if _, err := r.VNC.DeletePublicIp(ctx, ocicore.DeletePublicIpRequest{
		PublicIpId: ocicommon.String(eip.Status.OCID),
	}); err != nil {
		if strings.Contains(err.Error(), "NotAuthorizedOrNotFound") {
			log.Info("ReservedIP not found; assuming ReservedIP is already released", "OCID", eip.Status.OCID)
		} else {
			return err
		}
	}

	log.Info("released")

	if eip.Status.EphemeralIPWasUnassigned {
		return r.assignEphemeralIP(ctx, eip, log)
	}

	return nil
}

func (r *ReservedIPReconciler) getPodPrivateIP(ctx context.Context, namespace, podName string) (string, error) {
	pod := &corev1.Pod{}
	if err := r.Get(ctx, client.ObjectKey{
		Namespace: namespace,
		Name:      podName,
	}, pod); err != nil {
		return "", err
	}

	return pod.Status.PodIP, nil
}

func (r *ReservedIPReconciler) getPrivateIPID(ctx context.Context, privateIP string) (string, error) {
	subnets, err := r.VNC.ListSubnets(ctx, ocicore.ListSubnetsRequest{
		CompartmentId: ocicommon.String(r.CompartmentID),
		VcnId:         ocicommon.String(r.VcnID),
	})
	if err != nil {
		return "", err
	}

	for _, subnet := range subnets.Items {
		_, cidr, err := net.ParseCIDR(*subnet.CidrBlock)
		if err != nil {
			return "", err
		}
		if !cidr.Contains(net.ParseIP(privateIP)) {
			continue
		}

		resp, err := r.VNC.ListPrivateIps(ctx, ocicore.ListPrivateIpsRequest{
			IpAddress: ocicommon.String(privateIP),
			SubnetId:  subnet.Id,
		})
		if err != nil {
			return "", err
		}
		if len(resp.Items) == 1 {
			return *resp.Items[0].Id, nil
		}
	}

	return "", fmt.Errorf("Private IP %s not found in VCN %s", privateIP, r.VcnID)
}

func (r *ReservedIPReconciler) assignReservedIP(ctx context.Context, reservedIP *ociv1alpha1.ReservedIP, log logr.Logger) error {
	if (reservedIP.Spec.Assignment.PodName == "") == (reservedIP.Spec.Assignment.PrivateIPAddress == "") {
		return fmt.Errorf("exactly one of spec.assignment.{podName,privateIPAddress} needs to be defined")
	}

	var err error
	privateIP := reservedIP.Spec.Assignment.PrivateIPAddress
	if reservedIP.Spec.Assignment.PodName != "" {
		privateIP, err = r.getPodPrivateIP(ctx, reservedIP.Namespace, reservedIP.Spec.Assignment.PodName)
		if err != nil {
			return err
		}
	}

	privateIPID, err := r.getPrivateIPID(ctx, privateIP)
	if err != nil {
		return err
	}

	publicIP, err := r.VNC.GetPublicIpByPrivateIpId(ctx, ocicore.GetPublicIpByPrivateIpIdRequest{
		GetPublicIpByPrivateIpIdDetails: ocicore.GetPublicIpByPrivateIpIdDetails{
			PrivateIpId: &privateIPID,
		},
	})
	if err != nil {
		if !strings.Contains(err.Error(), "NotAuthorizedOrNotFound") {
			return err
		} // no public IP is assigned to the private IP -> just continue
	} else {
		if publicIP.Id == &reservedIP.Status.OCID { // correct public IP already assigned
			return nil
		}

		if publicIP.Lifetime == ocicore.PublicIpLifetimeEphemeral {
			log.Info("deleting emphemeral public IP previously assigned to private IP",
				"podName", reservedIP.Spec.Assignment.PodName,
				"privateIP", privateIP,
				"privateIPID", privateIPID,
				"previousPublicIPID", *publicIP.Id)
			reservedIP.Status.EphemeralIPWasUnassigned = true
			if err := r.Status().Update(ctx, reservedIP); err != nil {
				return err
			}
			_, err = r.VNC.DeletePublicIp(ctx, ocicore.DeletePublicIpRequest{
				PublicIpId: publicIP.Id,
			})
			if err != nil {
				return err
			}
		} else {
			log.Info("unassigning reserved IP previously assigned to private IP",
				"podName", reservedIP.Spec.Assignment.PodName,
				"privateIP", privateIP,
				"privateIPID", privateIPID,
				"previousPublicIPID", *publicIP.Id)
			_, err = r.VNC.UpdatePublicIp(ctx, ocicore.UpdatePublicIpRequest{
				PublicIpId: publicIP.Id,
				UpdatePublicIpDetails: ocicore.UpdatePublicIpDetails{
					PrivateIpId: ocicommon.String(""),
				},
			})
			if err != nil {
				return err
			}
		}
	}

	log.Info("assigning public IP to private IP", "podName", reservedIP.Spec.Assignment.PodName, "privateIP", privateIP, "privateIPID", privateIPID)

	_, err = r.VNC.UpdatePublicIp(ctx, ocicore.UpdatePublicIpRequest{
		PublicIpId: ocicommon.String(reservedIP.Status.OCID),
		UpdatePublicIpDetails: ocicore.UpdatePublicIpDetails{
			PrivateIpId: &privateIPID,
		},
	})
	if err != nil {
		return err
	}

	log.Info("assigned")

	reservedIP.Status.State = "assigned"
	reservedIP.Status.Assignment = reservedIP.Spec.Assignment
	reservedIP.Status.Assignment.PrivateIPAddress = privateIP
	reservedIP.Status.PrivateIPAddressID = privateIPID
	if err := r.Status().Update(ctx, reservedIP); err != nil {
		return err
	}

	return nil
}

func (r *ReservedIPReconciler) unassignReservedIP(ctx context.Context, reservedIP *ociv1alpha1.ReservedIP, log logr.Logger) error {
	log.Info("unassigning")

	_, err := r.VNC.UpdatePublicIp(ctx, ocicore.UpdatePublicIpRequest{
		PublicIpId: ocicommon.String(reservedIP.Status.OCID),
		UpdatePublicIpDetails: ocicore.UpdatePublicIpDetails{
			PrivateIpId: ocicommon.String(""),
		},
	})
	if err != nil {
		return err
	}

	log.Info("unassigned")

	if reservedIP.Status.EphemeralIPWasUnassigned {
		if err := r.assignEphemeralIP(ctx, reservedIP, log); err != nil {
			return err
		}
	}

	reservedIP.Status.State = "allocated"
	reservedIP.Status.Assignment = nil
	reservedIP.Status.PrivateIPAddressID = ""
	if err := r.Status().Update(ctx, reservedIP); err != nil {
		return err
	}

	return nil
}

func (r *ReservedIPReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&ociv1alpha1.ReservedIP{}).
		Complete(r)
}
