#!/usr/bin/env python3
"""
Benchmark Gallery/Image Generator

Creates test galleries and images for benchmarking thumbnail storage modes.
Each image displays its ID number.

Usage:
    python3 generate_benchmark.py [--galleries 100] [--images-per-gallery 100] [--output benchmark.zip]
"""

import argparse
import os
import zipfile
import io
from PIL import Image, ImageDraw, ImageFont
import random
import string

def generate_image(image_id: int, width: int = 320, height: int = 240) -> bytes:
    """Generate a simple image with the image ID displayed."""
    img = Image.new('RGB', (width, height), color=(
        random.randint(50, 200),
        random.randint(50, 200),
        random.randint(50, 200)
    ))
    
    draw = ImageDraw.Draw(img)
    
    # Try to use a default font, fallback to built-in
    try:
        font = ImageFont.truetype("arial.ttf", 80)
    except:
        font = ImageFont.load_default()
    
    # Draw the image ID number
    text = str(image_id)
    bbox = draw.textbbox((0, 0), text, font=font)
    text_width = bbox[2] - bbox[0]
    text_height = bbox[3] - bbox[1]
    
    x = (width - text_width) // 2
    y = (height - text_height) // 2
    
    # Draw text with outline for visibility
    draw.text((x-2, y-2), text, fill='black', font=font)
    draw.text((x+2, y-2), text, fill='black', font=font)
    draw.text((x-2, y+2), text, fill='black', font=font)
    draw.text((x+2, y+2), text, fill='black', font=font)
    draw.text((x, y), text, fill='white', font=font)
    
    # Save to bytes
    buf = io.BytesIO()
    img.save(buf, format='JPEG', quality=85)
    return buf.getvalue()

def generate_gallery_zip(num_galleries: int, images_per_gallery: int, output_file: str):
    """Generate a zip file with galleries and images."""
    
    total_images = num_galleries * images_per_gallery
    print(f"Generating {num_galleries} galleries with {images_per_gallery} images each ({total_images} total)")
    print(f"Output: {output_file}")
    
    with zipfile.ZipFile(output_file, 'w', zipfile.ZIP_DEFLATED) as zf:
        image_id = 1
        
        for gallery_idx in range(1, num_galleries + 1):
            gallery_name = f"Gallery {gallery_idx:04d}"
            
            # Create gallery file
            gallery_folder = f"{gallery_name}/"
            
            for img_idx in range(1, images_per_gallery + 1):
                # Generate image data
                img_data = generate_image(image_id)
                
                # Add image to zip (using stash's expected path format)
                img_filename = f"{gallery_folder}{image_id:06d}.jpg"
                zf.writestr(img_filename, img_data)
                
                if image_id % 100 == 0:
                    print(f"  Generated {image_id}/{total_images} images...")
                
                image_id += 1
    
    print(f"Done! Generated {total_images} images in {num_galleries} galleries.")
    print(f"File size: {os.path.getsize(output_file) / 1024 / 1024:.1f} MB")

def main():
    parser = argparse.ArgumentParser(description='Generate benchmark galleries and images')
    parser.add_argument('--galleries', type=int, default=100, help='Number of galleries to create')
    parser.add_argument('--images-per-gallery', type=int, default=100, help='Images per gallery')
    parser.add_argument('--output', type=str, default='benchmark.zip', help='Output zip file')
    parser.add_argument('--width', type=int, default=320, help='Image width')
    parser.add_argument('--height', type=int, default=240, help='Image height')
    
    args = parser.parse_args()
    
    generate_gallery_zip(args.galleries, args.images_per_gallery, args.output)

if __name__ == '__main__':
    main()
