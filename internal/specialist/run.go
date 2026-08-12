package specialist

// The generic tool-runner loop: one Specialist plus a question becomes a
// Claude call with that specialist's scoped system prompt, tool subset, and
// effort setting. This is the in-process entry point.
//
// Each invocation logs the question and the specialist chosen. Dropping the
// explicit router cost us a separable routing decision to inspect; this log is
// what keeps misroutes diagnosable.
//
// Build step 7.
