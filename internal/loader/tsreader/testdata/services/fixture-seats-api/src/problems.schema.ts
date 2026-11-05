import { auth, behaviorOperations } from "@superschematic/api";
import { Problem } from "@schemas/fixture-seats-db";

// A problem's seats and status operations (D104), for any signed-in
// caller: the core decides who sits in each seat and who moves the problem.
@behaviorOperations(Problem)
@auth
export abstract class ProblemActions {}
