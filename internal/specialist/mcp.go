package specialist

// Wraps the same registry as MCP tools — ask_materials_specialist,
// ask_grades_specialist, and so on. tools/list becomes the specialist
// registry, and the orchestrator's native tool selection becomes the routing.
// There is no router component.
//
// Each Specialist's Purpose becomes the MCP tool description, which is the
// only signal the orchestrator reads when choosing. Write it as a trigger
// condition ("Call this when the user asks about grades, feedback, or what
// score they need"), not a label.
//
// Build step 8.
