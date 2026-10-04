import { useEffect, useRef } from 'react';

const LERP_SPEED = 0.3; // Speed coefficient for client-side interpolation

function Cursor({ color, x, y, label }) {
  const cursorRef = useRef(null);
  const currentPos = useRef({ x, y });

  useEffect(() => {
    let animationFrameId;

    const render = () => {
      // Lerp logic: current = current + (target - current) * speed
      currentPos.current.x += (x - currentPos.current.x) * LERP_SPEED;
      currentPos.current.y += (y - currentPos.current.y) * LERP_SPEED;

      if (cursorRef.current) {
        // Apply transform via hardware acceleration for smoothness
        cursorRef.current.style.transform = `translate(${currentPos.current.x}px, ${currentPos.current.y}px)`;
      }

      animationFrameId = requestAnimationFrame(render);
    };

    animationFrameId = requestAnimationFrame(render);
    return () => cancelAnimationFrame(animationFrameId);
  }, [x, y]);

  return (
    <div
      ref={cursorRef}
      style={{
        position: 'absolute',
        top: 0,
        left: 0,
        pointerEvents: 'none',
        zIndex: 1000,
      }}
    >
      <svg
        width="24"
        height="36"
        viewBox="0 0 24 36"
        fill="none"
        xmlns="http://www.w3.org/2000/svg"
        style={{
          transform: 'translate(-4px, -4px)',
          filter: 'drop-shadow(0px 2px 4px rgba(0,0,0,0.2))'
        }}
      >
        <path
          d="M5.65376 2.15376C5.4057 1.90569 5 2.08133 5 2.43257V26.2737C5 26.657 5.48514 26.8291 5.72765 26.5381L10.3667 20.9712C10.5186 20.7888 10.7483 20.6865 10.9868 20.6975L18.4312 21.043C18.8252 21.0613 19.0435 20.5902 18.775 20.3216L5.65376 2.15376Z"
          fill={color}
          stroke="white"
          strokeWidth="2"
        />
      </svg>
      <div
        style={{
          position: 'absolute',
          top: '24px',
          left: '12px',
          backgroundColor: color,
          color: 'white',
          padding: '4px 8px',
          borderRadius: '4px',
          fontSize: '12px',
          fontWeight: 600,
          whiteSpace: 'nowrap',
          boxShadow: '0 2px 4px rgba(0,0,0,0.1)',
          fontFamily: 'Inter, system-ui, sans-serif'
        }}
      >
        {label}
      </div>
    </div>
  );
}

export default Cursor;
