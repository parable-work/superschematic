// Plain is NOT decorated @trait. Field-bearing classes cannot be implemented
// directly: TypeScript demands member re-declaration (TS2720), and superschematic
// does not suppress that diagnostic. Field-bearing traits are implemented
// through the Trait<T> heritage carrier instead.
export abstract class Plain {
  status: string;
}

export abstract class Widget implements Plain {
  name: string;
}
