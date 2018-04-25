import datetime
import hashlib
import json
import os
import shutil
import subprocess

import dateutil.parser as date_parser
import exifread
import rawkit

import photodb.util as util


class EXIFMetadata(object):

    def __init__(self, tags):
        self.tags = tags

        width, height = get_exif_dimensions(tags)

        self.summary = {
            'make': get_exif_make(tags),
            'model': get_exif_model(tags),
            'focal_length': tags['FocalLength'],
            'width': width,
            'height': height,
            'timestamp': get_exif_timestamp(tags),
            'iso': get_exif_iso(tags),
            'shutter_speed': get_exif_shutter(tags),
            'aperture': get_exif_aperture(tags)
        }

    def __repr__(self):
        return repr(self.summary)


class Photo(object):

    def __init__(self, path):
        self.path = path
        self.file_ext = util.get_file_extension(self.path)

        self.metadata_type = util.get_type_from_ext(self.file_ext)
        self.metadata = read_exif(self.path)

    @property
    def checksum(self):
        return util.get_checksum(self.path)



def open_photo(path):
    pass

# ----------------------------------------------------------------------
# EXIF metadata processing


def read_exif(path):
    exiftool_output = subprocess.check_output(['exiftool', '-j', path])
    tags = json.loads(exiftool_output)[0]

    return EXIFMetadata(tags)


def get_exif_iso(tags):
    return tags['ISO']


def get_exif_shutter(tags):
    return tags['ShutterSpeedValue']


def get_exif_aperture(tags):
    return tags['FNumber']


def get_exif_dimensions(tags):
    if 'RawImageFullWidth' in tags:
        width = tags['RawImageFullWidth']
        height = tags['RawImageFullHeight']
    else:
        width = tags['ImageWidth']
        height = tags['ImageHeight']

    return width, height


def get_exif_make(tags):
    return tags['Make']


def get_exif_model(tags):
    return tags['Model']


EXIF_TIMESTAMP_FORMAT = '%Y:%m:%d %H:%M:%S'


def get_exif_timestamp(tags):
    timestamp = tags['CreateDate']
    return datetime.datetime.strptime(timestamp,
                                      EXIF_TIMESTAMP_FORMAT)
