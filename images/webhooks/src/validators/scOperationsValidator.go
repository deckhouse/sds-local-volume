package validators

import (
	"encoding/json"
	"k8s.io/api/admission/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"net/http"
)

const (
	localCSIProvisioner = "local.csi.storage.deckhouse.io"
)

func ValidateSCOperation(w http.ResponseWriter, r *http.Request) {
	arReview := v1beta1.AdmissionReview{}
	if err := json.NewDecoder(r.Body).Decode(&arReview); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	} else if arReview.Request == nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	raw := arReview.Request.Object.Raw

	arReview.Response = &v1beta1.AdmissionResponse{
		UID:     arReview.Request.UID,
		Allowed: true,
	}

	var jsonData map[string]interface{}
	err := json.Unmarshal(raw, &jsonData)
	if err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if jsonData["provisioner"] == localCSIProvisioner {
		if arReview.Request.UserInfo.Username == "system:serviceaccount:d8-sds-local-volume:sds-local-volume-controller" {
			arReview.Response.Allowed = true
			klog.Infof("Incoming request approved (%s)", string(raw))
		} else if arReview.Request.Operation == "DELETE" {
			arReview.Response.Allowed = true
			klog.Infof("Incoming request approved (%s)", string(raw))
		} else {
			arReview.Response.Allowed = false
			arReview.Response.Result = &metav1.Status{
				Message: "Manual operations with this StorageClass is prohibited. Please use LocalStorageClass instead.",
			}
			klog.Infof("Incoming request denied: Manual operations with this StorageClass is prohibited. Please use LocalStorageClass instead (%s)", string(raw))
		}
	} else {
		arReview.Response.Allowed = true
	}

	w.Header().Set("Content-Type", "application/json")
	err = json.NewEncoder(w).Encode(&arReview)
	if err != nil {
		http.Error(w, "invalid response body", http.StatusInternalServerError)
		return
	}
}
