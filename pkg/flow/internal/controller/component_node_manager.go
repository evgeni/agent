package controller

import (
	"fmt"
	"strings"

	"github.com/grafana/river/ast"
)

// ComponentNodeManager is a manager that manages component nodes.
type ComponentNodeManager struct {
	importNodes                 map[string]*ImportConfigNode
	declareNodes                map[string]*DeclareNode
	globals                     ComponentGlobals
	componentReg                ComponentRegistry
	customComponentDependencies map[string][]CustomComponentDependency
	parentDeclareContents       map[string]string
}

// NewComponentNodeManager creates a new ComponentNodeManager.
func NewComponentNodeManager(globals ComponentGlobals, componentReg ComponentRegistry) *ComponentNodeManager {
	return &ComponentNodeManager{
		importNodes:                 map[string]*ImportConfigNode{},
		declareNodes:                map[string]*DeclareNode{},
		customComponentDependencies: map[string][]CustomComponentDependency{},
		globals:                     globals,
		componentReg:                componentReg,
	}
}

// OnReload resets the state of the component node manager.
func (m *ComponentNodeManager) OnReload(parentDeclareContents map[string]string) {
	m.parentDeclareContents = parentDeclareContents
	m.customComponentDependencies = make(map[string][]CustomComponentDependency)
	m.importNodes = map[string]*ImportConfigNode{}
	m.declareNodes = map[string]*DeclareNode{}
}

// CreateComponentNode creates a new builtin component or a new custom component.
func (m *ComponentNodeManager) CreateComponentNode(componentName string, block *ast.BlockStmt) (ComponentNode, error) {
	firstPart := strings.Split(componentName, ".")[0]
	if m.shouldAddCustomComponentNode(firstPart, componentName) {
		return NewCustomComponentNode(m.globals, block, m.getCustomComponentConfig), nil
	} else {
		registration, exists := m.componentReg.Get(componentName)
		if !exists {
			return nil, fmt.Errorf("unrecognized component name %q", componentName)
		}
		return NewBuiltinComponentNode(m.globals, registration, block), nil
	}
}

// GetCustomComponentDependencies retrieves and caches the dependencies that declare might have to other declares.
func (m *ComponentNodeManager) getCustomComponentDependencies(declareNode *DeclareNode) ([]CustomComponentDependency, error) {
	var dependencies []CustomComponentDependency
	if deps, ok := m.customComponentDependencies[declareNode.label]; ok {
		dependencies = deps
	} else {
		var err error
		dependencies, err = m.FindCustomComponentDependencies(declareNode.Declare())
		if err != nil {
			return nil, err
		}
		m.customComponentDependencies[declareNode.label] = dependencies
	}
	return dependencies, nil
}

// shouldAddCustomComponentNode searches for a declare corresponding to the given component name.
func (m *ComponentNodeManager) shouldAddCustomComponentNode(firstPart, componentName string) bool {
	_, declareExists := m.declareNodes[firstPart]
	_, importExists := m.importNodes[firstPart]
	_, parentDeclareContentExists := m.parentDeclareContents[componentName]

	return declareExists || importExists || parentDeclareContentExists
}

func (m *ComponentNodeManager) GetCorrespondingLocalDeclare(cc *CustomComponentNode) (*DeclareNode, bool) {
	declareNode, exist := m.declareNodes[cc.declareLabel]
	return declareNode, exist
}

func (m *ComponentNodeManager) GetCorrespondingImportedDeclare(cc *CustomComponentNode) (*ImportConfigNode, bool) {
	importNode, exist := m.importNodes[cc.importLabel]
	return importNode, exist
}

// CustomComponentConfig represents the config needed by a custom component to load.
type CustomComponentConfig struct {
	declareContent            string            // represents the corresponding declare as plain string
	additionalDeclareContents map[string]string // represents the additional declare that might be needed by the component to build custom components
}

// getCustomComponentConfig returns the custom component config for a given custom component.
func (m *ComponentNodeManager) getCustomComponentConfig(cc *CustomComponentNode) (CustomComponentConfig, error) {
	var customComponentConfig CustomComponentConfig
	var found bool
	var err error
	if cc.importLabel == "" {
		customComponentConfig, found = m.getCustomComponentConfigFromLocalDeclares(cc)
		if !found {
			customComponentConfig, found = m.getCustomComponentConfigFromParent(cc)
		}
	} else {
		customComponentConfig, found, err = m.getCustomComponentConfigFromImportedDeclares(cc)
		if err != nil {
			return customComponentConfig, err
		}
		if !found {
			customComponentConfig, found = m.getCustomComponentConfigFromParent(cc)
			customComponentConfig.additionalDeclareContents = filterParentDeclareContents(cc.importLabel, customComponentConfig.additionalDeclareContents)
		}
	}
	if !found {
		return customComponentConfig, fmt.Errorf("custom component config not found for component %s", cc.componentName)
	}
	return customComponentConfig, nil
}

// getCustomComponentConfigFromLocalDeclares retrieves the config of a custom component from the local declares.
func (m *ComponentNodeManager) getCustomComponentConfigFromLocalDeclares(cc *CustomComponentNode) (CustomComponentConfig, bool) {
	node, exists := m.declareNodes[cc.declareLabel]
	if !exists {
		return CustomComponentConfig{}, false
	}
	return CustomComponentConfig{
		declareContent:            node.Declare().Content,
		additionalDeclareContents: m.getLocalAdditionalDeclareContents(cc.componentName),
	}, true
}

// getCustomComponentConfigFromParent retrieves the config of a custom component from the parent controller.
func (m *ComponentNodeManager) getCustomComponentConfigFromParent(cc *CustomComponentNode) (CustomComponentConfig, bool) {
	declareContent, exists := m.parentDeclareContents[cc.componentName]
	if !exists {
		return CustomComponentConfig{}, false
	}
	return CustomComponentConfig{
		declareContent:            declareContent,
		additionalDeclareContents: m.parentDeclareContents,
	}, true
}

// getImportedCustomComponentConfig retrieves the config of a custom component from the imported declares.
func (m *ComponentNodeManager) getCustomComponentConfigFromImportedDeclares(cc *CustomComponentNode) (CustomComponentConfig, bool, error) {
	node, exists := m.importNodes[cc.importLabel]
	if !exists {
		return CustomComponentConfig{}, false, nil
	}
	declare, err := node.GetImportedDeclareByLabel(cc.declareLabel)
	if err != nil {
		return CustomComponentConfig{}, false, err
	}
	return CustomComponentConfig{
		declareContent:            declare.Content,
		additionalDeclareContents: m.getImportAdditionalDeclareContents(node),
	}, true, nil
}

// getImportAdditionalDeclareContents provides the additional declares that a custom component might need.
func (m *ComponentNodeManager) getImportAdditionalDeclareContents(node *ImportConfigNode) map[string]string {
	additionalDeclareContents := make(map[string]string, len(node.ImportedDeclares()))
	for importedDeclareLabel, importedDeclare := range node.ImportedDeclares() {
		additionalDeclareContents[importedDeclareLabel] = importedDeclare.Content
	}
	return additionalDeclareContents
}

// getLocalAdditionalDeclareContents provides the additional declares that a custom component might need.
func (m *ComponentNodeManager) getLocalAdditionalDeclareContents(componentName string) map[string]string {
	additionalDeclareContents := make(map[string]string)
	for _, customComponentDependency := range m.customComponentDependencies[componentName] {
		if customComponentDependency.importNode != nil {
			for importedDeclareLabel, importedDeclare := range customComponentDependency.importNode.ImportedDeclares() {
				// The label of the importNode is added as a prefix to the declare label to create a scope.
				// This is useful in the scenario where a custom component of an imported declare is defined inside of a local declare.
				// In this case, this custom component should only have have access to the imported declares of its corresponding import node.
				additionalDeclareContents[customComponentDependency.importNode.label+"."+importedDeclareLabel] = importedDeclare.Content
			}
		} else if customComponentDependency.declareNode != nil {
			additionalDeclareContents[customComponentDependency.declareLabel] = customComponentDependency.declareNode.Declare().Content
		} else {
			additionalDeclareContents[customComponentDependency.componentName] = m.parentDeclareContents[customComponentDependency.componentName]
		}
	}
	return additionalDeclareContents
}

// filterParentDeclareContents prevents custom components from accessing declared content out of their scope.
func filterParentDeclareContents(importLabel string, parentDeclareContents map[string]string) map[string]string {
	filteredParentDeclareContents := make(map[string]string)
	for declareLabel, declareContent := range parentDeclareContents {
		// The scope is defined by the importLabel prefix in the declareLabel of the declare block.
		if strings.HasPrefix(declareLabel, importLabel) {
			filteredParentDeclareContents[strings.TrimPrefix(declareLabel, importLabel+".")] = declareContent
		}
	}
	return filteredParentDeclareContents
}

// CustomComponentDependency represents a dependency that a custom component has to a declare block.
type CustomComponentDependency struct {
	componentName string
	importLabel   string
	declareLabel  string
	importNode    *ImportConfigNode
	declareNode   *DeclareNode
}

// FindCustomComponentDependencies traverses the AST of the provided declare and collects references to known custom components.
// Panics if declare is nil.
func (m *ComponentNodeManager) FindCustomComponentDependencies(declare *Declare) ([]CustomComponentDependency, error) {
	uniqueReferences := make(map[string]CustomComponentDependency)
	m.findCustomComponentDependencies(declare.Block.Body, uniqueReferences)

	references := make([]CustomComponentDependency, 0, len(uniqueReferences))
	for _, ref := range uniqueReferences {
		references = append(references, ref)
	}

	return references, nil
}

func (m *ComponentNodeManager) findCustomComponentDependencies(stmts ast.Body, uniqueReferences map[string]CustomComponentDependency) {
	for _, stmt := range stmts {
		switch stmt := stmt.(type) {
		case *ast.BlockStmt:
			componentName := strings.Join(stmt.Name, ".")
			switch componentName {
			case "declare":
				m.findCustomComponentDependencies(stmt.Body, uniqueReferences)
			default:
				potentialImportLabel, potentialDeclareLabel := ExtractImportAndDeclareLabels(componentName)
				if declareNode, ok := m.declareNodes[potentialDeclareLabel]; ok {
					uniqueReferences[componentName] = CustomComponentDependency{componentName: componentName, importLabel: "", declareLabel: potentialDeclareLabel, declareNode: declareNode}
				} else if importNode, ok := m.importNodes[potentialImportLabel]; ok {
					uniqueReferences[componentName] = CustomComponentDependency{componentName: componentName, importLabel: potentialImportLabel, declareLabel: potentialDeclareLabel, importNode: importNode}
				} else if _, ok := m.parentDeclareContents[componentName]; ok {
					uniqueReferences[componentName] = CustomComponentDependency{componentName: componentName, importLabel: potentialImportLabel, declareLabel: potentialDeclareLabel}
				}
			}
		}
	}
}
