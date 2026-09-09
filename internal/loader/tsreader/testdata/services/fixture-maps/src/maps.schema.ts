export abstract class MapContainer {
  strings: Record<string, string>;
  nested: Record<string, MapValue>;
}

export abstract class MapValue {
  count: number;
}
