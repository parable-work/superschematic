export type SchemaClass<TInstance = unknown> = abstract new (...args: never[]) => TInstance;

export type SchemaRef<TInstance = unknown> = {
  readonly schemaRef: string;
  readonly __instance?: TInstance;
};

export function schemaOf<TInstance>(schemaClass: SchemaClass<TInstance>): SchemaRef<TInstance> {
  return { schemaRef: schemaClass.name };
}
