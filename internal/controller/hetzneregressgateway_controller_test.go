/*
Copyright 2026 Maarlab Rethinking.
Licensed under the Apache License, Version 2.0 (the "License").
*/

package controller

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	egressv1alpha1 "github.com/maarlab-rethinking/hcloud-egress-gateway-controller/api/v1alpha1"
	"github.com/maarlab-rethinking/hcloud-egress-gateway-controller/internal/hcloud"
	"github.com/maarlab-rethinking/hcloud-egress-gateway-controller/internal/names"
)

// fakeHCloud is an in-memory hcloud.Client: EnsureManaged is idempotent by
// (gateway, location, index) so re-resolving never creates duplicates.
type fakeHCloud struct {
	created    map[string]hcloud.FloatingIP
	byAddr     map[string]hcloud.FloatingIP
	nextID     int64
	assigned   map[int64]int64
	unassigned []int64
	deleted    []int64
}

func newFakeHCloud() *fakeHCloud {
	return &fakeHCloud{
		created:  map[string]hcloud.FloatingIP{},
		byAddr:   map[string]hcloud.FloatingIP{},
		assigned: map[int64]int64{},
	}
}

func (f *fakeHCloud) EnsureManaged(_ context.Context, gateway, location string, index int, _ string, _ map[string]string) (hcloud.FloatingIP, error) {
	k := fmt.Sprintf("%s|%s|%d", gateway, location, index)
	if v, ok := f.created[k]; ok {
		return v, nil
	}
	f.nextID++
	fip := hcloud.FloatingIP{ID: f.nextID, Address: fmt.Sprintf("203.0.113.%d", f.nextID), Location: location}
	f.created[k] = fip
	return fip, nil
}

func (f *fakeHCloud) ListManaged(_ context.Context, gateway string) ([]hcloud.FloatingIP, error) {
	var out []hcloud.FloatingIP
	for k, v := range f.created {
		if strings.HasPrefix(k, gateway+"|") {
			v.AssignedToID = f.assigned[v.ID]
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *fakeHCloud) GetByAddress(_ context.Context, address string) (hcloud.FloatingIP, error) {
	if v, ok := f.byAddr[address]; ok {
		return v, nil
	}
	return hcloud.FloatingIP{}, fmt.Errorf("floating IP %s not found", address)
}

func (f *fakeHCloud) Assign(_ context.Context, ipID, serverID int64) error {
	f.assigned[ipID] = serverID
	return nil
}

func (f *fakeHCloud) Unassign(_ context.Context, ipID int64) error {
	delete(f.assigned, ipID)
	f.unassigned = append(f.unassigned, ipID)
	return nil
}

func (f *fakeHCloud) Delete(_ context.Context, ipID int64) error {
	f.deleted = append(f.deleted, ipID)
	for k, v := range f.created {
		if v.ID == ipID {
			delete(f.created, k)
		}
	}
	return nil
}

func newTestReconciler(fh hcloud.Client, objs ...client.Object) *Reconciler {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = egressv1alpha1.AddToScheme(s)
	c := fake.NewClientBuilder().WithScheme(s).
		WithStatusSubresource(&egressv1alpha1.HetznerEgressGateway{}).
		WithObjects(objs...).Build()
	return &Reconciler{
		Client:          c,
		Scheme:          s,
		HCloud:          fh,
		AgentImage:      "img:tag",
		AgentSA:         "egress-sa",
		Namespace:       "egress-system",
		TokenSecretName: "hcloud-egress-token",
		TokenSecretKey:  "token",
	}
}

func TestResolveIPsManagedMultiRegionIdempotent(t *testing.T) {
	fh := newFakeHCloud()
	heg := &egressv1alpha1.HetznerEgressGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gw"},
		Spec: egressv1alpha1.HetznerEgressGatewaySpec{
			Managed: &egressv1alpha1.ManagedFloatingIPs{Regions: []egressv1alpha1.ManagedRegion{
				{Location: "nbg1", Count: 2}, {Location: "fsn1", Count: 1},
			}},
		},
	}
	r := newTestReconciler(fh, heg)

	regions, err := r.resolveIPs(context.Background(), heg)
	if err != nil {
		t.Fatal(err)
	}
	if len(regions) != 2 || regions[0].location != "nbg1" || len(regions[0].ips) != 2 ||
		regions[1].location != "fsn1" || len(regions[1].ips) != 1 {
		t.Fatalf("unexpected regions: %+v", regions)
	}
	// Each managed IP carries the region it was created in.
	if regions[0].ips[0].Location != "nbg1" {
		t.Fatalf("ip location = %q, want nbg1", regions[0].ips[0].Location)
	}

	// Idempotent: a second resolve reuses the same IPs (never recreates).
	before := fh.nextID
	regions2, _ := r.resolveIPs(context.Background(), heg)
	if fh.nextID != before {
		t.Fatalf("resolve created new IPs on second call: %d -> %d", before, fh.nextID)
	}
	if regions2[0].ips[0].ID != regions[0].ips[0].ID {
		t.Fatalf("non-idempotent IDs: %d vs %d", regions2[0].ips[0].ID, regions[0].ips[0].ID)
	}
}

func TestResolveIPsBYOGroupsByLocation(t *testing.T) {
	fh := newFakeHCloud()
	fh.byAddr = map[string]hcloud.FloatingIP{
		"1.1.1.1": {ID: 1, Address: "1.1.1.1", Location: "nbg1"},
		"2.2.2.2": {ID: 2, Address: "2.2.2.2", Location: "fsn1"},
		"3.3.3.3": {ID: 3, Address: "3.3.3.3", Location: "nbg1"},
	}
	heg := &egressv1alpha1.HetznerEgressGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gw"},
		Spec:       egressv1alpha1.HetznerEgressGatewaySpec{FloatingIPs: []string{"1.1.1.1", "2.2.2.2", "3.3.3.3"}},
	}
	r := newTestReconciler(fh, heg)

	regions, err := r.resolveIPs(context.Background(), heg)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, reg := range regions {
		counts[reg.location] = len(reg.ips)
	}
	if counts["nbg1"] != 2 || counts["fsn1"] != 1 {
		t.Fatalf("BYO grouping by location wrong: %v", counts)
	}
}

func TestEnsureStatefulSetsPerRegionAndGC(t *testing.T) {
	heg := &egressv1alpha1.HetznerEgressGateway{ObjectMeta: metav1.ObjectMeta{Name: "gw"}}
	r := newTestReconciler(newFakeHCloud(), heg)
	ctx := context.Background()

	regions := []regionIPs{
		{location: "nbg1", ips: []hcloud.FloatingIP{{ID: 1, Address: "1.1.1.1"}}},
		{location: "fsn1", ips: []hcloud.FloatingIP{{ID: 2, Address: "2.2.2.2"}, {ID: 3, Address: "3.3.3.3"}}},
	}
	if err := r.ensureStatefulSets(ctx, heg, regions); err != nil {
		t.Fatal(err)
	}

	var list appsv1.StatefulSetList
	if err := r.List(ctx, &list, client.InNamespace("egress-system")); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 2 {
		t.Fatalf("want 2 StatefulSets, got %d", len(list.Items))
	}

	var fsn appsv1.StatefulSet
	if err := r.Get(ctx, client.ObjectKey{Namespace: "egress-system", Name: "egress-agent-gw-fsn1"}, &fsn); err != nil {
		t.Fatalf("fsn1 StatefulSet: %v", err)
	}
	if fsn.Spec.Replicas == nil || *fsn.Spec.Replicas != 2 {
		t.Fatalf("fsn1 replicas = %v, want 2", fsn.Spec.Replicas)
	}
	if got := fsn.Spec.Template.Spec.NodeSelector[names.RegionLabelKey]; got != "fsn1" {
		t.Fatalf("fsn1 nodeSelector region = %q, want fsn1", got)
	}
	if !fsn.Spec.Template.Spec.HostNetwork {
		t.Fatal("agent must run hostNetwork")
	}
	c := fsn.Spec.Template.Spec.Containers[0]
	if c.SecurityContext == nil || c.SecurityContext.Privileged == nil || !*c.SecurityContext.Privileged {
		t.Fatal("agent must run privileged")
	}
	// Must run as root: a privileged container running as non-root has an empty
	// effective-cap set and can't create the egr0 interface (EPERM).
	if c.SecurityContext.RunAsUser == nil || *c.SecurityContext.RunAsUser != 0 {
		t.Fatalf("agent must run as root (runAsUser 0), got %v", c.SecurityContext.RunAsUser)
	}
	if got := envValue(c.Env, "EGRESS_FLOATING_IPS"); got != "2:2.2.2.2,3:3.3.3.3" {
		t.Fatalf("EGRESS_FLOATING_IPS = %q", got)
	}

	// GC: dropping fsn1 from the spec deletes its StatefulSet.
	if err := r.ensureStatefulSets(ctx, heg, regions[:1]); err != nil {
		t.Fatal(err)
	}
	list = appsv1.StatefulSetList{}
	_ = r.List(ctx, &list, client.InNamespace("egress-system"))
	if len(list.Items) != 1 || list.Items[0].Name != "egress-agent-gw-nbg1" {
		t.Fatalf("GC failed, remaining: %v", stsNames(list))
	}
}

func TestFinalizeHonorsReclaimPolicy(t *testing.T) {
	newHEG := func(policy egressv1alpha1.ReclaimPolicy) *egressv1alpha1.HetznerEgressGateway {
		return &egressv1alpha1.HetznerEgressGateway{
			ObjectMeta: metav1.ObjectMeta{Name: "gw", Finalizers: []string{names.Finalizer}},
			Spec: egressv1alpha1.HetznerEgressGatewaySpec{
				Managed: &egressv1alpha1.ManagedFloatingIPs{
					ReclaimPolicy: policy,
					Regions:       []egressv1alpha1.ManagedRegion{{Location: "nbg1", Count: 1}, {Location: "fsn1", Count: 1}},
				},
			},
		}
	}

	// Retain: leave the floating IPs untouched.
	fhRetain := newFakeHCloud()
	hegRetain := newHEG(egressv1alpha1.ReclaimRetain)
	if err := newTestReconciler(fhRetain, hegRetain).finalize(context.Background(), hegRetain); err != nil {
		t.Fatal(err)
	}
	if len(fhRetain.deleted) != 0 {
		t.Fatalf("Retain must not delete IPs, deleted: %v", fhRetain.deleted)
	}

	// Delete: release every managed floating IP of the gateway, including one a lowered
	// spec no longer asks for (hel1 here), but not another gateway's.
	fhDelete := newFakeHCloud()
	hegDelete := newHEG(egressv1alpha1.ReclaimDelete)
	for _, loc := range []string{"nbg1", "fsn1", "hel1"} {
		_, _ = fhDelete.EnsureManaged(context.Background(), "gw", loc, 0, "", nil)
	}
	other, _ := fhDelete.EnsureManaged(context.Background(), "other", "nbg1", 0, "", nil)
	if err := newTestReconciler(fhDelete, hegDelete).finalize(context.Background(), hegDelete); err != nil {
		t.Fatal(err)
	}
	if len(fhDelete.deleted) != 3 || slices.Contains(fhDelete.deleted, other.ID) {
		t.Fatalf("Delete must release the gateway's 3 IPs and no other, deleted: %v", fhDelete.deleted)
	}
}

func TestReleaseSurplusIPs(t *testing.T) {
	ctx := context.Background()
	newHEG := func(policy egressv1alpha1.ReclaimPolicy, regions ...egressv1alpha1.ManagedRegion) *egressv1alpha1.HetznerEgressGateway {
		return &egressv1alpha1.HetznerEgressGateway{
			ObjectMeta: metav1.ObjectMeta{Name: "gw"},
			Spec: egressv1alpha1.HetznerEgressGatewaySpec{
				Managed: &egressv1alpha1.ManagedFloatingIPs{ReclaimPolicy: policy, Regions: regions},
			},
		}
	}
	// A gateway that had nbg1 x2, fsn1 and hel1, all assigned to gateway nodes.
	seed := func() (*fakeHCloud, map[string]hcloud.FloatingIP) {
		fh := newFakeHCloud()
		ips := map[string]hcloud.FloatingIP{}
		for _, k := range []struct {
			loc string
			i   int
		}{{"nbg1", 0}, {"nbg1", 1}, {"fsn1", 0}, {"hel1", 0}} {
			fip, _ := fh.EnsureManaged(ctx, "gw", k.loc, k.i, "", nil)
			fh.assigned[fip.ID] = 100 + fip.ID
			ips[fmt.Sprintf("%s-%d", k.loc, k.i)] = fip
		}
		return fh, ips
	}

	t.Run("Retain unassigns and reports what the spec dropped", func(t *testing.T) {
		fh, ips := seed()
		// fsn1 and hel1 removed, nbg1 lowered from 2 to 1.
		heg := newHEG(egressv1alpha1.ReclaimRetain, egressv1alpha1.ManagedRegion{Location: "nbg1", Count: 1})
		r := newTestReconciler(fh, heg)
		regions, err := r.resolveIPs(ctx, heg)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.releaseSurplusIPs(ctx, heg, regions); err != nil {
			t.Fatal(err)
		}
		if len(fh.deleted) != 0 {
			t.Fatalf("Retain must not delete, deleted: %v", fh.deleted)
		}
		if _, ok := fh.assigned[ips["nbg1-0"].ID]; !ok {
			t.Fatal("the address still in the spec was unassigned")
		}
		want := []string{ips["nbg1-1"].Address, ips["fsn1-0"].Address, ips["hel1-0"].Address}
		if !slices.Equal(heg.Status.RetainedFloatingIPs, want) {
			t.Fatalf("retainedFloatingIPs = %v, want %v", heg.Status.RetainedFloatingIPs, want)
		}
		for _, k := range []string{"nbg1-1", "fsn1-0", "hel1-0"} {
			if _, ok := fh.assigned[ips[k].ID]; ok {
				t.Fatalf("%s is still assigned", k)
			}
		}

		// A second pass changes nothing: the retained addresses are already unassigned.
		fh.unassigned = nil
		if err := r.releaseSurplusIPs(ctx, heg, regions); err != nil {
			t.Fatal(err)
		}
		if len(fh.unassigned) != 0 || len(heg.Status.RetainedFloatingIPs) != 3 {
			t.Fatalf("second pass: unassigned %v, retained %v", fh.unassigned, heg.Status.RetainedFloatingIPs)
		}

		// Asking for hel1 again re-adopts its address, which leaves the retained list.
		heg.Spec.Managed.Regions = append(heg.Spec.Managed.Regions, egressv1alpha1.ManagedRegion{Location: "hel1", Count: 1})
		regions, _ = r.resolveIPs(ctx, heg)
		if err := r.releaseSurplusIPs(ctx, heg, regions); err != nil {
			t.Fatal(err)
		}
		if slices.Contains(heg.Status.RetainedFloatingIPs, ips["hel1-0"].Address) {
			t.Fatalf("re-adopted hel1 still listed as retained: %v", heg.Status.RetainedFloatingIPs)
		}
	})

	t.Run("Delete deletes what the spec dropped", func(t *testing.T) {
		fh, ips := seed()
		heg := newHEG(egressv1alpha1.ReclaimDelete, egressv1alpha1.ManagedRegion{Location: "nbg1", Count: 1})
		r := newTestReconciler(fh, heg)
		regions, _ := r.resolveIPs(ctx, heg)
		if err := r.releaseSurplusIPs(ctx, heg, regions); err != nil {
			t.Fatal(err)
		}
		want := []int64{ips["nbg1-1"].ID, ips["fsn1-0"].ID, ips["hel1-0"].ID}
		if !slices.Equal(fh.deleted, want) {
			t.Fatalf("deleted = %v, want %v", fh.deleted, want)
		}
		if len(heg.Status.RetainedFloatingIPs) != 0 {
			t.Fatalf("Delete retains nothing, got %v", heg.Status.RetainedFloatingIPs)
		}
	})

	t.Run("BYO addresses are never touched", func(t *testing.T) {
		// Even a labelled address from an earlier managed spec stays as it is.
		fh, _ := seed()
		heg := &egressv1alpha1.HetznerEgressGateway{ObjectMeta: metav1.ObjectMeta{Name: "gw"}}
		if err := newTestReconciler(fh, heg).releaseSurplusIPs(ctx, heg, nil); err != nil {
			t.Fatal(err)
		}
		if len(fh.unassigned)+len(fh.deleted) != 0 {
			t.Fatalf("BYO touched: unassigned %v, deleted %v", fh.unassigned, fh.deleted)
		}
	})
}

func TestGCAndStatusDropsStaleNodeLabelsAndReports(t *testing.T) {
	heg := &egressv1alpha1.HetznerEgressGateway{ObjectMeta: metav1.ObjectMeta{Name: "gw"}}
	gatewayNode := func(name, ip string, ready corev1.ConditionStatus) *corev1.Node {
		return &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{
				names.NodeLabelKey: "gw", names.FloatingIPLabelKey: ip,
			}},
			Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: ready}}},
		}
	}
	agentPod := func(name, node string) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "egress-system", Labels: map[string]string{crLabelKey: "gw"}},
			Spec:       corev1.PodSpec{NodeName: node},
		}
	}
	r := newTestReconciler(newFakeHCloud(), heg,
		gatewayNode("node-a", "203.0.113.1", corev1.ConditionTrue),
		gatewayNode("node-b", "203.0.113.2", corev1.ConditionTrue),
		gatewayNode("node-c", "203.0.113.3", corev1.ConditionUnknown),
		agentPod("egress-agent-gw-nbg1-0", "node-a"),
		agentPod("egress-agent-gw-fsn1-0", "node-c"),
	)
	ctx := context.Background()

	if err := r.gcAndStatus(ctx, heg); err != nil {
		t.Fatal(err)
	}

	// node-b has no agent pod (recycled), and node-c is not Ready although its agent pod is
	// still bound to it (a dead node): both lose their gateway labels so the policy fails
	// over to node-a.
	for _, name := range []string{"node-b", "node-c"} {
		var n corev1.Node
		_ = r.Get(ctx, client.ObjectKey{Name: name}, &n)
		if _, ok := n.Labels[names.NodeLabelKey]; ok {
			t.Fatalf("gateway label on %s was not dropped", name)
		}
		if _, ok := n.Labels[names.FloatingIPLabelKey]; ok {
			t.Fatalf("floating-ip label on %s was not dropped", name)
		}
	}
	// node-a keeps its labels and is reported.
	if heg.Status.ReadyGateways != 1 {
		t.Fatalf("readyGateways = %d, want 1", heg.Status.ReadyGateways)
	}
	if len(heg.Status.FloatingIPs) != 1 || heg.Status.FloatingIPs[0].AssignedNode != "node-a" ||
		heg.Status.FloatingIPs[0].Address != "203.0.113.1" {
		t.Fatalf("unexpected status.floatingIPs: %+v", heg.Status.FloatingIPs)
	}
}

func envValue(env []corev1.EnvVar, name string) string {
	for _, e := range env {
		if e.Name == name {
			return e.Value
		}
	}
	return ""
}

func stsNames(l appsv1.StatefulSetList) []string {
	var out []string
	for i := range l.Items {
		out = append(out, l.Items[i].Name)
	}
	return out
}
