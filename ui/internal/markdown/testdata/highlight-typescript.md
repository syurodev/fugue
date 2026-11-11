```typescript
interface Point { x: number; y: number }

@decorator()
export function dist(p: Point): number {
  const re = /ab+c/gi;
  return Math.sqrt(p.x ** 2 + /* inline */ p.y ** 2);
}
```
