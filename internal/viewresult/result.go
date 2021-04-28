package viewresult

import (
	"bytes"
	"context"
	"fmt"
	goerrors "github.com/pkg/errors"
	"strings"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8sserial "k8s.io/apimachinery/pkg/runtime/serializer/json"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/JAORMX/oc-compliance/internal/common"
	"github.com/jaytaylor/html2text"
	"github.com/olekukonko/tablewriter"
)

const (
	ruleAnnotationKey       = "compliance.openshift.io/rule"
	controlAnnotationPrefix = "control.compliance.openshift.io/"
)

type ResultHelper struct {
	kuser common.KubeClientUser
	gvk   schema.GroupVersionResource
	kind  string
	name  string
	genericclioptions.IOStreams
	table *tablewriter.Table
}

func NewResultHelper(kuser common.KubeClientUser, name string, streams genericclioptions.IOStreams) common.ObjectHelper {
	table := tablewriter.NewWriter(streams.Out)
	table.SetHeader([]string{"Key", "Value"})
	table.SetAutoWrapText(true)
	table.SetReflowDuringAutoWrap(false)
	table.SetAutoMergeCells(true)
	table.SetRowLine(true)

	return &ResultHelper{
		kuser: kuser,
		name:  name,
		kind:  "ComplianceCheckResult",
		gvk: schema.GroupVersionResource{
			Group:    common.CmpAPIGroup,
			Version:  common.CmpResourceVersion,
			Resource: "compliancecheckresults",
		},
		IOStreams: streams,
		table:     table,
	}
}

func (h *ResultHelper) Handle() error {
	res, err := h.kuser.DynamicClient().Resource(h.gvk).Namespace(h.kuser.GetNamespace()).Get(context.TODO(), h.name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	resultAnns := res.GetAnnotations()
	if resultAnns == nil {
		return fmt.Errorf("Result had no annotations, couldn't determine rule.")
	}
	ruleRef, ok := resultAnns[ruleAnnotationKey]
	if !ok {
		return fmt.Errorf("Malformed result. It doesn't contain a rule reference.")
	}

	rule, err := h.getRule(res, ruleRef)
	if err != nil {
		return err
	}

	if err := h.stringToTable(rule, "title"); err != nil {
		return err
	}
	if err := h.stringToTable(res, "status"); err != nil {
		return err
	}

	if err := h.stringToTable(res, "severity"); err != nil {
		return err
	}

	if err := h.htmlStringToTable(rule, "description"); err != nil {
		return err
	}
	if err := h.htmlStringToTable(rule, "rationale"); err != nil {
		return err
	}
	if err := h.stringToTableIfExists(res, "instructions"); err != nil {
		return err
	}

	h.displayControls(rule)

	if err := h.displayAvailableFixes(rule); err != nil {
		return err
	}

	h.table.Append([]string{"Result Object Name", res.GetName()})
	h.table.Append([]string{"Rule Object Name", rule.GetName()})

	rem, err := h.getRemediation(res)
	if err != nil {
		return err
	}

	if rem != nil {
		h.table.Append([]string{"Remediation Created", "Yes"})
		h.table.Append([]string{"Remediation Name", rem.GetName()})
		str, found, err := unstructured.NestedString(rem.Object, "status", "applicationState")
		if err != nil {
			return fmt.Errorf("Unable to get %s of %s/%s of type %s: %s", "applicationState", rem.GetNamespace(), rem.GetName(), rem.GetKind(), err)
		}
		if !found {
			return fmt.Errorf("%s/%s of type %s: has no '%s'", rem.GetNamespace(), rem.GetName(), rem.GetKind(), "applicationState")
		}

		h.table.Append([]string{"Remediation Status", str})
	} else {
		h.table.Append([]string{"Remediation Created", "No"})
	}
	h.table.Render()
	return nil
}

func (h *ResultHelper) stringToTableEx(obj *unstructured.Unstructured, mustExist bool, keys ...string) error {
	str, found, err := unstructured.NestedString(obj.Object, keys...)
	lastKey := keys[len(keys)-1]
	if err != nil {
		return fmt.Errorf("Unable to get %s of %s/%s of type %s: %s", lastKey, obj.GetNamespace(), obj.GetName(), obj.GetKind(), err)
	}
	if !found {
		if !mustExist {
			return nil
		}
		return fmt.Errorf("%s/%s of type %s: has no '%s'", obj.GetNamespace(), obj.GetName(), obj.GetKind(), lastKey)
	}

	h.table.Append([]string{strings.Title(lastKey), str})
	return nil
}

func (h *ResultHelper) stringToTable(obj *unstructured.Unstructured, keys ...string) error {
	return h.stringToTableEx(obj, true, keys...)
}

func (h *ResultHelper) htmlStringToTableEx(obj *unstructured.Unstructured, mustExist bool, keys ...string) error {
	xmlStr, found, err := unstructured.NestedString(obj.Object, keys...)
	lastKey := keys[len(keys)-1]
	if err != nil {
		return fmt.Errorf("Unable to get %s of %s/%s of type %s: %s", lastKey, obj.GetNamespace(), obj.GetName(), obj.GetKind(), err)
	}
	if !found {
		if !mustExist {
			return nil
		}
		return fmt.Errorf("%s/%s of type %s: has no '%s'", obj.GetNamespace(), obj.GetName(), obj.GetKind(), lastKey)
	}

	htmlStr := strings.ReplaceAll(xmlStr, "<html:", "<")
	htmlStr = strings.ReplaceAll(htmlStr, "</html:", "</")
	text, err := html2text.FromString(htmlStr, html2text.Options{PrettyTables: true, OmitLinks: false})
	if err != nil {
		text = xmlStr
	}
	h.table.Append([]string{strings.Title(lastKey), text})
	return nil
}

func (h *ResultHelper) htmlStringToTable(obj *unstructured.Unstructured, keys ...string) error {
	return h.htmlStringToTableEx(obj, true, keys...)
}

func (h *ResultHelper) htmlStringToTableIfExists(obj *unstructured.Unstructured, keys ...string) error {
	return h.htmlStringToTableEx(obj, false, keys...)
}
func (h *ResultHelper) stringToTableIfExists(obj *unstructured.Unstructured, keys ...string) error {
	return h.stringToTableEx(obj, false, keys...)
}

func (h *ResultHelper) displayControls(rule *unstructured.Unstructured) {
	annotations := rule.GetAnnotations()
	if annotations == nil {
		// non-fatal... but no controls to display
		return
	}

	for key, value := range annotations {
		if strings.HasPrefix(key, controlAnnotationPrefix) {
			benchmark := key[len(controlAnnotationPrefix):]
			bmtext := fmt.Sprintf("%s Controls", benchmark)
			commaSeparatedControls := strings.ReplaceAll(value, ";", ", ")
			h.table.Append([]string{bmtext, commaSeparatedControls})
		}
	}
}

func (h *ResultHelper) displayAvailableFixes(rule *unstructured.Unstructured) error {
	fixes, found, err := unstructured.NestedSlice(rule.Object, "availableFixes")
	if err != nil {
		return fmt.Errorf("Unable to get %s of %s/%s of type %s: %s", "avaliableFixes", rule.GetNamespace(), rule.GetName(), rule.GetKind(), err)
	}
	if found && fixes != nil {
		h.table.Append([]string{"Avalailable Fix", "Yes"})
		for _, fixObjRaw := range fixes {
			fixObj, ok := fixObjRaw.(map[string]interface{})
			if !ok {
				return fmt.Errorf("Unable to parse %s of %s/%s of type %s: %s", "fixObject", rule.GetNamespace(), rule.GetName(), rule.GetKind(), err)
			}
			fix, found, err := unstructured.NestedMap(fixObj, "fixObject")
			if err != nil {
				return fmt.Errorf("Unable to get %s of %s/%s of type %s: %s", "fixObject", rule.GetNamespace(), rule.GetName(), rule.GetKind(), err)
			}
			if !found {
				return fmt.Errorf("%s/%s of type %s: has no '%s'", rule.GetNamespace(), rule.GetName(), rule.GetKind(), "fixObject")
			}

			yamlSerializer := k8sserial.NewYAMLSerializer(k8sserial.DefaultMetaFactory, nil, nil)
			objToPersist := &unstructured.Unstructured{Object: fix}
			buf := bytes.Buffer{}
			common.PersistObjectToYaml("", objToPersist, &buf, yamlSerializer)
			h.table.Append([]string{"Fix Object", buf.String()})
		}
	} else {
		h.table.Append([]string{"Avalailable Fix", "No"})
	}

	return nil
}

func (h *ResultHelper) getRule(res *unstructured.Unstructured, ruleRef string) (*unstructured.Unstructured, error) {
	scan, err := getControllerOf(res, h.kuser)
	if err != nil {
		return nil, err
	}
	scanProfileXCCDFID, err := getProfileIDFromScan(scan)
	if err != nil {
		return nil, err
	}
	scanDSFile, err := getDSFromScan(scan)
	if err != nil {
		return nil, err
	}
	spi := scanProfileID{scanDSFile, scanProfileXCCDFID}
	suite, err := getControllerOf(scan, h.kuser)
	if err != nil {
		return nil, goerrors.Wrapf(err, "cannot get a suite that owns scan %s", scan.GetName())
	}
	binding, err := getControllerOf(suite, h.kuser)
	if err != nil {
		return nil, goerrors.Wrapf(err, "cannot get a binding that owns suite %s", suite.GetName())
	}
	profs, err := h.getProfiles(binding)
	if err != nil {
		return nil, err
	}
	ph, err := h.findRelevantProfile(profs, binding, spi)
	if err != nil {
		return nil, err
	}
	return ph.FindRule(ruleRef)
}

func (h *ResultHelper) findRelevantProfile(profs []interface{}, binding *unstructured.Unstructured, spi scanProfileID) (profileHandler, error) {
	for _, rawProf := range profs {
		profRef, ok := rawProf.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("Error parsing profiles from ScanSettingBinding '%s' that this result belongs to", binding.GetName())
		}
		gvr := getGVRFromProfileRef(profRef)
		profname := profRef["name"].(string)
		prof, err := h.kuser.DynamicClient().Resource(gvr).Namespace(h.kuser.GetNamespace()).Get(context.TODO(), profname, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		ph, err := getProfileHandler(prof, binding.GetName(), h.kuser)
		if err != nil {
			return nil, err
		}
		if ph.ProfileMatches(spi) {
			return ph, nil
		}
	}

	return nil, fmt.Errorf("Didn't find relevant profile")
}

// Get a profile from a scansettingBinding object
func (h *ResultHelper) getProfiles(obj *unstructured.Unstructured) ([]interface{}, error) {
	profs, found, err := unstructured.NestedSlice(obj.Object, "profiles")
	if err != nil {
		return nil, fmt.Errorf("Unable to get profiles of %s/%s of type %s: %s", obj.GetNamespace(), obj.GetName(), "ComplianceCheckResult", err)
	}
	if !found {
		return nil, fmt.Errorf("%s/%s of type %s: has no 'profiles'", obj.GetNamespace(), obj.GetName(), "Profile")
	}
	return profs, nil
}

func (h *ResultHelper) getRemediation(obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	remgvr := schema.GroupVersionResource{
		Group:    common.CmpAPIGroup,
		Version:  common.CmpResourceVersion,
		Resource: "complianceremediations",
	}
	// First lets search for a remediation with the same name
	rem, err := h.kuser.DynamicClient().Resource(remgvr).Namespace(h.kuser.GetNamespace()).Get(context.TODO(), obj.GetName(), metav1.GetOptions{})
	if err != nil && !errors.IsNotFound(err) {
		return nil, err
	}
	// TODO(jaosorior): Handle remediations with no matching name by iterating
	// and checking owner references
	return rem, nil
}

func getProfileHandler(obj *unstructured.Unstructured, parent string, k common.KubeClientUser) (profileHandler, error) {
	rulegvr := schema.GroupVersionResource{
		Group:    common.CmpAPIGroup,
		Version:  common.CmpResourceVersion,
		Resource: "rules",
	}

	switch obj.GetKind() {
	case "Profile":
		return &profileHandlerImpl{k, obj, rulegvr}, nil
	case "TailoredProfile":
		return &tailoredProfileHandlerImpl{k, obj, rulegvr, nil}, nil
	}
	return nil, fmt.Errorf("Got unkown type for profile '%s' in parent object '%s'", obj.GetName(), parent)
}

type profileHandler interface {
	ProfileMatches(scanProfileID) bool
	FindRule(string) (*unstructured.Unstructured, error)
}

type profileHandlerImpl struct {
	kuser   common.KubeClientUser
	obj     *unstructured.Unstructured
	rulegvr schema.GroupVersionResource
}

func (ph *profileHandlerImpl) ProfileMatches(spi scanProfileID) bool {
	objid, found, err := unstructured.NestedString(ph.obj.Object, "id")
	if err != nil || !found {
		return false
	}
	pb, err := getControllerOf(ph.obj, ph.kuser)
	if err != nil {
		// TODO(jaosorior): Should probably issue a warning
		return false
	}
	contentFile, found, err := unstructured.NestedString(pb.Object, "spec", "contentFile")
	if err != nil || !found {
		// TODO(jaosorior): Should probably issue a warning
		return false
	}
	profspi := scanProfileID{contentFile, objid}
	return spi.IsEqual(profspi)
}

func (ph *profileHandlerImpl) FindRule(ruleRef string) (*unstructured.Unstructured, error) {
	rules, err := common.GetRulesFromProfile(ph.obj)
	if err != nil {
		return nil, err
	}

	for _, rule := range rules {
		// OPTIMIZATION: don't fetch the rule, the annotation should be a
		// substring of the rule name
		if !strings.Contains(rule, ruleRef) {
			continue
		}
		ruleobj, err := ph.kuser.DynamicClient().Resource(ph.rulegvr).Namespace(ph.kuser.GetNamespace()).Get(context.TODO(), rule, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		ruleAnns := ruleobj.GetAnnotations()
		// TODO(jaosorior): Should a warning be printed if a rule has no annotatins?
		if ruleAnns == nil {
			continue
		}
		if ruleRef == ruleAnns[ruleAnnotationKey] {
			return ruleobj, nil
		}
	}

	return nil, fmt.Errorf("Didn't find relevant rule for extra information")
}

type tailoredProfileHandlerImpl struct {
	kuser         common.KubeClientUser
	obj           *unstructured.Unstructured
	rulegvr       schema.GroupVersionResource
	parentProfile *unstructured.Unstructured
}

func (tph *tailoredProfileHandlerImpl) ProfileMatches(spi scanProfileID) bool {
	objid, found, err := unstructured.NestedString(tph.obj.Object, "status", "id")
	if err != nil || !found {
		return false
	}
	prof, err := tph.getParentProfile()
	if err != nil {
		// TODO(jaosorior): Should probably issue a warning
		return false
	}
	pb, err := getControllerOf(prof, tph.kuser)
	if err != nil {
		// TODO(jaosorior): Should probably issue a warning
		return false
	}
	contentFile, found, err := unstructured.NestedString(pb.Object, "spec", "contentFile")
	if err != nil || !found {
		// TODO(jaosorior): Should probably issue a warning
		return false
	}
	profspi := scanProfileID{contentFile, objid}
	return spi.IsEqual(profspi)
}

func (tph *tailoredProfileHandlerImpl) FindRule(ruleRef string) (*unstructured.Unstructured, error) {
	prof, err := tph.getParentProfile()
	if err != nil {
		return nil, err
	}

	ph, err := getProfileHandler(prof, tph.obj.GetName(), tph.kuser)
	if err != nil {
		return nil, err
	}
	return ph.FindRule(ruleRef)
}

func (tph *tailoredProfileHandlerImpl) getParentProfile() (*unstructured.Unstructured, error) {
	if tph.parentProfile != nil {
		return tph.parentProfile, nil
	}
	profname, err := getProfileFromTailoredProfile(tph.obj)
	if err != nil {
		return nil, err
	}
	profgvr := schema.GroupVersionResource{
		Group:    common.CmpAPIGroup,
		Version:  common.CmpResourceVersion,
		Resource: "profiles",
	}
	prof, err := tph.kuser.DynamicClient().Resource(profgvr).Namespace(tph.kuser.GetNamespace()).Get(context.TODO(), profname, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	tph.parentProfile = prof
	return tph.parentProfile, nil
}

// Represents the necessary info to uniquely identify a profile
type scanProfileID struct {
	file string
	id   string
}

func (spi scanProfileID) IsEqual(other scanProfileID) bool {
	return spi.file == other.file && spi.id == other.id
}

// Helper Functions
// ================

func getControllerOf(res *unstructured.Unstructured, k common.KubeClientUser) (*unstructured.Unstructured, error) {
	ctrl := metav1.GetControllerOf(res)
	if ctrl == nil {
		return nil, fmt.Errorf("the object had no owner")
	}

	gvr := getGVRFromAPIVersionAndKind(ctrl.APIVersion, ctrl.Kind)
	return k.DynamicClient().Resource(gvr).Namespace(k.GetNamespace()).Get(context.TODO(), ctrl.Name, metav1.GetOptions{})
}

func getGVRFromProfileRef(profRef map[string]interface{}) schema.GroupVersionResource {
	// NOTE: We wrongly named the apiVersion to be apiGroup
	apiVersion := profRef["apiGroup"].(string)
	kind := profRef["kind"].(string)
	return getGVRFromAPIVersionAndKind(apiVersion, kind)
}

func getGVRFromAPIVersionAndKind(apiVersion, kind string) schema.GroupVersionResource {
	gvk := schema.FromAPIVersionAndKind(apiVersion, kind)
	return gvk.GroupVersion().WithResource(pluralizeKind(gvk.Kind))
}

func pluralizeKind(kind string) string {
	ret := strings.ToLower(kind)
	if strings.HasSuffix(ret, "s") {
		return fmt.Sprintf("%ses", ret)
	}
	return fmt.Sprintf("%ss", ret)
}

func getProfileIDFromScan(obj *unstructured.Unstructured) (string, error) {
	id, found, err := unstructured.NestedString(obj.Object, "spec", "profile")
	if err != nil {
		return "", fmt.Errorf("Unable to get profile id of %s/%s of type %s: %s", obj.GetNamespace(), obj.GetName(), "ComplianceScan", err)
	}
	if !found {
		return "", fmt.Errorf("%s/%s of type %s: has no 'profile'", obj.GetNamespace(), obj.GetName(), "ComplianceScan")
	}
	return id, nil
}

func getDSFromScan(obj *unstructured.Unstructured) (string, error) {
	fil, found, err := unstructured.NestedString(obj.Object, "spec", "content")
	if err != nil {
		return "", fmt.Errorf("Unable to get content file reference of %s/%s of type %s: %s", obj.GetNamespace(), obj.GetName(), "ComplianceScan", err)
	}
	if !found {
		return "", fmt.Errorf("%s/%s of type %s: has no 'content'", obj.GetNamespace(), obj.GetName(), "ComplianceScan")
	}
	return fil, nil
}

func getProfileFromTailoredProfile(obj *unstructured.Unstructured) (string, error) {
	prof, found, err := unstructured.NestedString(obj.Object, "spec", "extends")
	if err != nil {
		return "", fmt.Errorf("Unable to get profile name from %s/%s of type %s: %s", obj.GetNamespace(), obj.GetName(), obj.GetKind(), err)
	}
	if !found {
		return "", fmt.Errorf("%s/%s of type %s: has no profile reference", obj.GetNamespace(), obj.GetName(), obj.GetKind())
	}
	return prof, nil
}
